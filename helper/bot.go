package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// BOT 一个已连接的客户端（机器）对象。
// 每个 BOT 由一个独立协程（listener.go 的读循环）服务，协程之间互不通信。
type BOT struct {
	ID   string // botID：首次上线由服务端分配，客户端保存到本地 config，之后靠它识别身份
	IP   string
	Port int

	Conn *websocket.Conn
	wmu  sync.Mutex // 保护 Conn 的并发写（命令下发、心跳确认等来自不同协程）

	LastBeat atomic.Int64 // 最近一次收到心跳的 UnixNano 时间戳
	Missed   int          // 连续未收到心跳的检测周期数
	Buffered bool         // 是否已进入缓存区列表

	OnlineAt time.Time   // 上线时间（bots -l 用）
	OutC     chan []byte // 终端输出字节流，终端窗口（attachserver.go）消费

	attached atomic.Bool // 是否已有终端窗口接入该 bot（同一机器同时只开一个窗口）

	blog *logx.BotLog // 该 bot 的专属日志
}

// newBOT 构造 BOT 对象并打开其专属 botlog
func newBOT(id, ip string, port int, conn *websocket.Conn, logDir string) (*BOT, error) {
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
		OutC:     make(chan []byte, 256),
		blog:     blog,
	}
	b.LastBeat.Store(time.Now().UnixNano())
	return b, nil
}

// Send 并发安全地向该 bot 发送一条消息
func (b *BOT) Send(msg *protocol.Message) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return b.Conn.WriteJSON(msg)
}

// Addr 返回该 bot 的来源地址（IP:Port）
func (b *BOT) Addr() string { return fmt.Sprintf("%s:%d", b.IP, b.Port) }

// PushOut 把终端输出推入队列，bot 面板在线时展示；队列满则丢弃（内容已写入 botlog）
func (b *BOT) PushOut(p []byte) {
	select {
	case b.OutC <- p:
	default:
	}
}

// DiscardOut 清空尚未消费的输出，避免下一个终端窗口读到上个窗口的残留
func (b *BOT) DiscardOut() {
	for {
		select {
		case <-b.OutC:
		default:
			return
		}
	}
}

// TryAttach 尝试占用该 bot 的终端窗口位：成功返回 true，
// 已有窗口打开时返回 false（避免多个窗口抢同一个终端输入）。
func (b *BOT) TryAttach() bool { return b.attached.CompareAndSwap(false, true) }

// ReleaseAttach 释放终端窗口位
func (b *BOT) ReleaseAttach() { b.attached.Store(false) }

// HasAttach 是否已有终端窗口打开
func (b *BOT) HasAttach() bool { return b.attached.Load() }
