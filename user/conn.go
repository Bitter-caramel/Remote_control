package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
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

// Dial 主动向协助者的服务器建立 TCP 连接，并升级为 WebSocket 长连接
func Dial(addr string) (*websocket.Conn, error) {
	u := "ws://" + addr + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
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
	if err := c.Send(&protocol.Message{Type: protocol.TypeRegister, UserID: c.cfg.UserID}); err != nil {
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
		}
	}
}

// WatchInterrupt 监听 Ctrl+C：本程序下线之前，先向协助者发送下线通知包（bye），
// 让对方能立即销毁对应协程，而不是等心跳超时。
func (c *Client) WatchInterrupt() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		fmt.Println("\n正在下线……")
		_ = c.Send(&protocol.Message{Type: protocol.TypeBye})
		time.Sleep(500 * time.Millisecond) // 给消息一点发送时间
		os.Exit(0)
	}()
}
