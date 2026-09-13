package agent

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-user/protocol"
)

// Client 到协助者服务器的连接，负责注册身份、转发终端输入输出
type Client struct {
	cfg   *Config
	Conn  *websocket.Conn
	wmu   sync.Mutex // 保护 Conn 的并发写（shell 输出、心跳协程）
	shell *ShellManager
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
		OS:     DetectOS(),
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

// ReadLoop 阻塞式读循环：接收协助者的键盘输入写入本机 shell，
// shell 的输出由 ShellManager.Pump 协程持续回传，直到连接断开。
func (c *Client) ReadLoop() {
	for {
		var msg protocol.Message
		if err := c.Conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case protocol.TypeInput:
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			if err := c.shell.Write(p); err != nil {
				// shell 启动失败时，把原因回显到远端终端
				notice := fmt.Sprintf("\r\n[启动 shell 失败: %v]\r\n", err)
				_ = c.Send(&protocol.Message{
					Type: protocol.TypeOutput,
					Data: protocol.EncodeB64([]byte(notice)),
				})
			}
		case protocol.TypeResize:
			if msg.Cols > 0 && msg.Rows > 0 {
				c.shell.Resize(msg.Cols, msg.Rows)
			}
		case protocol.TypeExec:
			// Agent/CLI 一次性命令：独立进程执行，不碰交互终端
			cmdBytes, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				c.replyExecResult(msg.MsgID, "[命令编码异常]", -1)
				continue
			}
			go func(m protocol.Message, line string) {
				out, code := execOneShot(line, m.Timeout)
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
