package ipc

// ipc：主程序内部的本地接入服务，含两条互不干扰的通道：
//
//	/attach —— 终端窗口通道（helper.exe -attach ...），把弹出窗口变成某台机器的远程终端；
//	/cli    —— CLI/Agent 通道（helper.exe -cli ...），请求/响应式执行一次性命令。
//
// 安全：只监听 127.0.0.1（外部网络不可达），端口随机分配，
// 接入必须携带启动时生成的随机令牌，且只允许本机回环来源。

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/internal/core"
	"remoteassist-helper/protocol"
)

// Server 本地接入服务
type Server struct {
	m            *core.Manager
	ln           net.Listener
	srv          *http.Server
	token        string
	endpointPath string // 发现文件路径（Close 时删除，空表示未写成功）
}

// NewServer 在 127.0.0.1 随机端口启动本地接入服务
func NewServer(m *core.Manager, logDir string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	a := &Server{m: m, ln: ln, token: hex.EncodeToString(buf)}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			// 非浏览器的 WebSocket 客户端不带 Origin；浏览器场景只放行本机回环来源
			origin := strings.ToLower(r.Header.Get("Origin"))
			return origin == "" ||
				strings.HasPrefix(origin, "http://127.0.0.1") ||
				strings.HasPrefix(origin, "http://localhost")
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/attach", a.handle(upgrader))
	mux.HandleFunc("/cli", a.handleCLI(upgrader))
	a.srv = &http.Server{Handler: mux}
	go a.srv.Serve(ln)

	// 把本机接入点（地址+令牌）落盘，供 helper.exe -cli 自动发现
	a.endpointPath = writeEndpointFile(logDir, a.Addr(), a.token)
	return a, nil
}

// Addr 本地监听地址（host:port），传给弹出的终端窗口进程
func (a *Server) Addr() string { return a.ln.Addr().String() }

// Token 接入令牌，传给弹出的终端窗口进程
func (a *Server) Token() string { return a.token }

// Close 停止本地接入服务
func (a *Server) Close() {
	a.srv.Close()
	if a.endpointPath != "" {
		os.Remove(a.endpointPath)
	}
}

func (a *Server) handle(upgrader websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		// 1. 第一条消息必须是接入请求（5 秒内）
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var req protocol.Message
		if err := conn.ReadJSON(&req); err != nil ||
			req.Type != protocol.TypeAttach || req.Data != a.token {
			conn.WriteJSON(protocol.Message{Type: protocol.TypeAttachAck, Data: "鉴权失败"})
			conn.Close()
			return
		}

		// 2. 找到对应在线 bot
		b := a.m.Get(req.UserID)
		if b == nil {
			conn.WriteJSON(protocol.Message{Type: protocol.TypeAttachAck, Data: "机器不在线"})
			conn.Close()
			return
		}

		// 3. 接入成功，订阅该 bot 的终端输出（先收到历史回放），开始桥接
		conn.SetReadDeadline(time.Time{})
		if err := conn.WriteJSON(protocol.Message{Type: protocol.TypeAttachAck, Data: "ok"}); err != nil {
			conn.Close()
			return
		}
		out, cancel := b.Subscribe()
		defer cancel()
		b.Log().Sys("终端窗口接入")

		done := make(chan struct{})
		defer close(done)
		readDone := make(chan struct{})

		// 窗口 → bot：键盘输入 / 尺寸变化
		go func() {
			defer close(readDone)
			for {
				var msg protocol.Message
				if err := conn.ReadJSON(&msg); err != nil {
					return
				}
				switch msg.Type {
				case protocol.TypeInput:
					p, err := protocol.DecodeB64(msg.Data)
					if err != nil {
						continue
					}
					if err := b.Send(&protocol.Message{Type: protocol.TypeInput, Data: protocol.EncodeB64(p)}); err != nil {
						return
					}
				case protocol.TypeResize:
					if msg.Cols > 0 && msg.Rows > 0 {
						_ = b.Send(&protocol.Message{Type: protocol.TypeResize, Cols: msg.Cols, Rows: msg.Rows})
					}
				}
			}
		}()

		// bot → 窗口：终端输出；机器下线或窗口关闭则结束
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-readDone:
				// 终端窗口已关闭，桥接结束
				b.Log().Sys("终端窗口关闭")
				return
			case p, ok := <-out:
				if !ok {
					// 订阅通道被关闭 = 机器已下线，通知窗口后结束
					// （此时 botlog 已随下线关闭，不再写日志）
					conn.WriteJSON(protocol.Message{Type: protocol.TypeBye})
					return
				}
				if err := conn.WriteJSON(protocol.Message{
					Type: protocol.TypeOutput,
					Data: protocol.EncodeB64(p),
				}); err != nil {
					b.Log().Sys("终端窗口关闭")
					return
				}
			case <-ticker.C:
				if a.m.Get(b.ID) == nil {
					conn.WriteJSON(protocol.Message{Type: protocol.TypeBye})
					b.Log().Sys("终端窗口因机器下线而关闭")
					return
				}
			}
		}
	}
}
