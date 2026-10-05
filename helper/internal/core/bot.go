package core

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

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

	// 操作上下文：每台机器上可同时存在多条互相隔离的操作线（见 context.go）。
	// 订阅通道的发送与关闭都在 ctxMu 内完成，避免向已关闭的通道写入。
	ctxMu sync.Mutex
	ctxs  map[string]*CtxState

	events *EventLog // 播报历史；由 Manager.Register 注入，可为 nil

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
		ctxs:     make(map[string]*CtxState),
		blog:     blog,
	}
	b.LastBeat.Store(time.Now().UnixNano())
	return b, nil
}

// SetEventLog 注入播报历史（由 Manager.Register 在机器上线、尚无订阅者时调用）
func (b *BOT) SetEventLog(e *EventLog) { b.events = e }

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

// pendingEvent 待写进播报历史的记录（锁内收集，锁外追加）
type pendingEvent struct {
	kind   EventKind
	who    Subscriber
	detail string
}

func (b *BOT) recordAll(evs []pendingEvent) {
	if b.events == nil || len(evs) == 0 {
		return
	}
	name := b.NameOrDash()
	for _, ev := range evs {
		b.events.Append(Event{
			Kind:     ev.kind,
			BotID:    b.ID,
			BotName:  name,
			Actor:    strings.TrimSpace(ev.who.Display),
			Username: ev.who.Username,
			Detail:   ev.detail,
		})
	}
}
