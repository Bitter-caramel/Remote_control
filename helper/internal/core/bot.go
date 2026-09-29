package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// outHistoryLimit 每个 bot 保留的终端输出回放上限（字节）。
// 新接入的终端窗口/浏览器会先收到这段回放，接入瞬间就能看到终端的当前画面，
// 而不是对着空白屏幕猜远端的状态。
const outHistoryLimit = 64 * 1024

// BOT 一个已连接的客户端（机器）对象。
// 每个 BOT 由一个独立协程（uplink 的读循环）服务，协程之间互不通信。
type BOT struct {
	ID   string // botID：首次上线由服务端分配，客户端保存到本地 config，之后靠它识别身份
	Name string // 被控机自定义名称（用户在 config.json 填写，可能为空）
	OS   string // 被控机系统信息（如 windows/amd64 (10.0.26200)）
	IP   string
	Port int

	Conn *websocket.Conn
	wmu  sync.Mutex // 保护 Conn 的并发写（命令下发、心跳确认等来自不同协程）

	LastBeat atomic.Int64 // 最近一次收到心跳的 UnixNano 时间戳
	Missed   int          // 连续未收到心跳的检测周期数
	Buffered bool         // 是否已进入缓存区列表

	OnlineAt time.Time // 上线时间（bots -l 用）

	// 终端输出的订阅者集合：本机终端窗口与浏览器可以同时接入同一台机器，
	// 所有订阅者都收到同一份输出，也都能向远端发送输入。
	subMu   sync.Mutex
	subs    map[int]chan []byte
	nextSub int
	hist    []byte // 最近输出的回放缓冲（上限 outHistoryLimit 字节）

	blog *logx.BotLog // 该 bot 的专属日志
}

// NewBOT 构造 BOT 对象并打开其专属 botlog
func NewBOT(id, ip string, port int, conn *websocket.Conn, logDir string) (*BOT, error) {
	blog, err := logx.NewBotLog(logDir, id)
	if err != nil {
		return nil, err
	}
	b := &BOT{
		ID:       id,
		IP:       ip,
		Port:     port,
		Conn:     conn,
		OnlineAt: time.Now(),
		subs:     make(map[int]chan []byte),
		blog:     blog,
	}
	b.LastBeat.Store(time.Now().UnixNano())
	return b, nil
}

// Log 该 bot 的专属日志（终端录像 + Agent 审计）
func (b *BOT) Log() *logx.BotLog { return b.blog }

// Send 并发安全地向该 bot 发送一条消息
func (b *BOT) Send(msg *protocol.Message) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return b.Conn.WriteJSON(msg)
}

// Addr 返回该 bot 的来源地址（IP:Port）
func (b *BOT) Addr() string { return fmt.Sprintf("%s:%d", b.IP, b.Port) }

// Title 带名称的显示标题：有自定义名称时为 "名称(botID)"，否则只有 botID
func (b *BOT) Title() string {
	if b.Name != "" {
		return b.Name + "(" + b.ID + ")"
	}
	return b.ID
}

// NameOrDash 名称列展示用：空名称显示 -
func (b *BOT) NameOrDash() string {
	if b.Name == "" {
		return "-"
	}
	return b.Name
}

// OSOrDash 系统列展示用：未知显示 -
func (b *BOT) OSOrDash() string {
	if b.OS == "" {
		return "-"
	}
	return b.OS
}

// Subscribe 注册一个终端输出订阅者（本机终端窗口或浏览器）。
// 返回的输出通道在 cancel 时被关闭；订阅瞬间会先收到一次历史回放。
func (b *BOT) Subscribe() (<-chan []byte, func()) {
	b.subMu.Lock()
	defer b.subMu.Unlock()

	id := b.nextSub
	b.nextSub++
	ch := make(chan []byte, 256)
	b.subs[id] = ch
	if len(b.hist) > 0 {
		ch <- append([]byte(nil), b.hist...)
	}

	cancel := func() {
		b.subMu.Lock()
		defer b.subMu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
	return ch, cancel
}

// PushOut 把终端输出广播给所有订阅者，并追加到回放缓冲。
// 订阅者来不及消费时直接丢弃该订阅者的这一份（内容已写入 botlog）。
func (b *BOT) PushOut(p []byte) {
	b.subMu.Lock()
	defer b.subMu.Unlock()

	b.hist = append(b.hist, p...)
	if len(b.hist) > outHistoryLimit {
		b.hist = append([]byte(nil), b.hist[len(b.hist)-outHistoryLimit:]...)
	}
	for _, ch := range b.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// CloseSubs 关闭全部订阅通道（机器下线时调用，让桥接协程自然退出）
func (b *BOT) CloseSubs() {
	b.subMu.Lock()
	defer b.subMu.Unlock()
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
