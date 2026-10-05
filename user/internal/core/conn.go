package core

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-user/internal/exec"
	"remoteassist-user/internal/sys"
	"remoteassist-user/internal/term"
	"remoteassist-user/protocol"
)

// Client 到协助者服务器的连接，负责注册身份、转发终端输入输出
type Client struct {
	cfg      *Config
	Conn     *websocket.Conn
	wmu      sync.Mutex           // 保护 Conn 的并发写（终端输出、心跳协程）
	Sessions *term.SessionManager // 进程级多操作上下文（跨重连保活，由 agent 注入）
}

// NewClient 组装一个到协助者服务器的连接（多上下文管理器由外层注入 Sessions）
func NewClient(cfg *Config, conn *websocket.Conn) *Client {
	return &Client{cfg: cfg, Conn: conn}
}

// Dial 主动向协助者的服务器建立 TCP 连接，并升级为 WebSocket 长连接。
// 带 10 秒握手超时：地址不可达（防火墙丢包/网线断开/旧配置残留）时
// 快速失败并进入重连，而不是无限期卡在"已读取本地身份"之后没有任何提示。
func Dial(addr string) (*websocket.Conn, error) {
	u := "ws://" + addr + "/ws"
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(u, nil)
	return conn, err
}

// Send 并发安全地发送一条消息
func (c *Client) Send(msg *protocol.Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.Conn.WriteJSON(msg)
}

// Register 发起注册：把 config 里的 userID 写入请求（首次为空），
// 服务端返回确认/分配的 userID。
func (c *Client) Register() (string, error) {
	if err := c.Send(&protocol.Message{
		Type:   protocol.TypeRegister,
		UserID: c.cfg.UserID,
		OS:     sys.DetectOS(),
		Name:   c.cfg.Name,
	}); err != nil {
		return "", err
	}
	c.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ack protocol.Message
	if err := c.Conn.ReadJSON(&ack); err != nil {
		return "", err
	}
	c.Conn.SetReadDeadline(time.Time{})
	if ack.Type != protocol.TypeRegisterAck || ack.UserID == "" {
		return "", errors.New("注册响应异常")
	}
	return ack.UserID, nil
}

// ReadLoop 阻塞式读循环：把协助者的消息按 CtxID 分发到对应操作上下文的 shell，
// 各上下文的输出由 SessionManager.Pump 协程持续回传，直到连接断开。
//
// 未知 / 缺失 CtxID 的终端类消息直接丢弃：上下文由服务端先发 TypeCtxOpen 建立。
func (c *Client) ReadLoop() {
	for {
		var msg protocol.Message
		if err := c.Conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeCtxOpen:
			c.Sessions.Open(msg.CtxID, msg.Cols, msg.Rows, msg.Cwd)
		case protocol.TypeInput:
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			_ = c.Sessions.Write(msg.CtxID, p)
		case protocol.TypeResize:
			c.Sessions.Resize(msg.CtxID, msg.Cols, msg.Rows)
		case protocol.TypeExec:
			// Agent/CLI 一次性命令：独立进程执行，不碰交互终端，与上下文无关
			cmdBytes, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				c.replyExecResult(msg.MsgID, "[命令编码异常]", -1)
				continue
			}
			go func(m protocol.Message, line string) {
				out, code := exec.ExecOneShot(line, m.Timeout)
				c.replyExecResult(m.MsgID, out, code)
			}(msg, string(cmdBytes))
		}
	}
}

// replyExecResult 回传一次性命令的执行结果
func (c *Client) replyExecResult(msgID, out string, code int) {
	_ = c.Send(&protocol.Message{
		Type:     protocol.TypeExecResult,
		MsgID:    msgID,
		Data:     protocol.EncodeB64([]byte(out)),
		ExitCode: code,
	})
}
