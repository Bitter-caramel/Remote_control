package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// upgrader 负责把收到的 TCP(HTTP) 连接升级为 WebSocket 长连接
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 允许任意来源的协助请求
}

// StartListener 启动服务监听。用户端主动向这里建立 TCP 连接并升级为 WebSocket，
// 之后每个连接（每个 bot 对象）由一个独立协程处理，协程之间互不通信。
func StartListener(addr string, m *Manager, prog *logx.ProgramLog) error {
	http.HandleFunc("/ws", wsHandler(m, prog))
	prog.Info("开始监听 %s", addr)
	return http.ListenAndServe(addr, nil)
}

// wsHandler 单个连接的升级与交接
func wsHandler(m *Manager, prog *logx.ProgramLog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			prog.Error("websocket 升级失败: %v", err)
			return
		}
		serveBot(conn, m, prog) // 独立协程服务这个 bot
	}
}

// serveBot 一个 bot 连接的完整生命周期，运行在独立协程：
// 注册握手 → 上线登记 → 读循环（心跳 / 回显 / 下线通知）
func serveBot(conn *websocket.Conn, m *Manager, prog *logx.ProgramLog) {
	// 1. 第一条消息必须是注册请求
	var reg protocol.Message
	if err := conn.ReadJSON(&reg); err != nil || reg.Type != protocol.TypeRegister {
		conn.Close()
		return
	}

	// 2. 确定 botID：首次上线（无 userID）则分配一个；老用户沿用其上报的 userID
	id := reg.UserID
	if id == "" {
		id = m.AllocID()
	}
	ra, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		prog.Error("无法识别的来源地址: %v", conn.RemoteAddr())
		conn.Close()
		return
	}
	b, err := newBOT(id, ra.IP.String(), ra.Port, conn, m.logDir)
	if err != nil {
		prog.Error("为 %s 创建 botlog 失败: %v", id, err)
		conn.Close()
		return
	}

	// 3. 应答注册结果（新机器把 botID 保存到本地 config）
	if err := b.Send(&protocol.Message{Type: protocol.TypeRegisterAck, UserID: id}); err != nil {
		prog.Error("向 %s 发送注册应答失败: %v", id, err)
		b.blog.Close()
		conn.Close()
		return
	}

	// 4. 上线登记：进 bots 列表、写 botslog
	m.Register(b)
	b.blog.Sys("上线 %s", b.Addr())
	fmt.Printf("[%s] 机器上线: %s (%s)\n", time.Now().Format("15:04:05"), b.ID, b.Addr())
	prog.Info("bot %s 上线 %s", b.ID, b.Addr())

	// 5. 读循环：本协程从此只服务于这个 bot，直到连接结束
	for {
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			m.Remove(b.ID, "连接断开")
			return
		}
		switch msg.Type {
		case protocol.TypeHeartbeat:
			b.LastBeat.Store(time.Now().UnixNano()) // 心跳只刷新存活时间，不写日志
		case protocol.TypeOutput:
			// 远端终端的原始输出：原样写入 botlog（终端录像），并推给在线的终端面板
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			b.blog.WriteTranscript(p)
			b.PushOut(p)
		case protocol.TypeBye:
			m.Remove(b.ID, "客户端主动下线")
			return
		}
	}
}
