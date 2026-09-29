package uplink

// uplink：被控端（user）连入侧的监听与接入。
// 每个连接进来后升级为 WebSocket，登记到 core.Manager，并转入独立协程读循环。

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// upgrader 负责把收到的 TCP(HTTP) 连接升级为 WebSocket 长连接
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 允许任意来源的协助请求
}

// NewListener 同步绑定对外监听端口（显式 IPv4）。
// 放在进入主面板之前调用：端口被占用时立即拿到错误给用户明确提示，
// 而不是后台 goroutine 悄悄退出（双击启动时会表现为黑框一闪）。
// 显式绑定 IPv4：若端口已被占用会立即报错（而不是在双栈系统上只绑到 IPv6、
// 让 IPv4 的客户端静默连到别的程序）。
func NewListener(addr string) (net.Listener, error) {
	return net.Listen("tcp4", addr)
}

// Serve 在已绑定的 listener 上阻塞地提供 HTTP 服务。
// 路由表由调用方组装（/ws 是用户端接入，其余为 Web 控制台等）。
func Serve(ln net.Listener, h http.Handler, prog *logx.ProgramLog) {
	prog.Info("开始监听 %s (IPv4)", ln.Addr().String())
	if err := http.Serve(ln, h); err != nil && err != http.ErrServerClosed {
		prog.Error("监听服务异常退出: %v", err)
		fmt.Printf("监听服务异常退出: %v\n", err)
	}
}

// Handler 用户端接入端点（WebSocket）：升级连接并交给 serveBot
func Handler(m *core.Manager, prog *logx.ProgramLog, st *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			prog.Error("websocket 升级失败: %v", err)
			return
		}
		serveBot(conn, m, prog, st) // 独立协程服务这个 bot
	})
}

// serveBot 一个 bot 连接的完整生命周期，运行在独立协程：
// 注册握手 → 上线登记 → 读循环（心跳 / 回显 / 下线通知）
func serveBot(conn *websocket.Conn, m *core.Manager, prog *logx.ProgramLog, st *store.Store) {
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
	b, err := core.NewBOT(id, ra.IP.String(), ra.Port, conn, m.LogDir())
	if err != nil {
		prog.Error("为 %s 创建 botlog 失败: %v", id, err)
		conn.Close()
		return
	}
	b.Name = reg.Name
	b.OS = reg.OS

	// 3. 应答注册结果（新机器把 botID 保存到本地 config）
	if err := b.Send(&protocol.Message{Type: protocol.TypeRegisterAck, UserID: id}); err != nil {
		prog.Error("向 %s 发送注册应答失败: %v", id, err)
		b.Log().Close()
		conn.Close()
		return
	}

	// 4. 上线登记：先落库（新机器按默认等级登记，老机器保留管理员设定的等级），
	// 再进 bots 列表、写 botslog
	if err := st.UpsertBot(id, b.Name, b.OS); err != nil {
		prog.Error("机器 %s 登记到数据库失败: %v", id, err)
	}
	m.Register(b)
	b.Log().Sys("上线 %s 名称=%s 系统=%s", b.Addr(), b.NameOrDash(), b.OSOrDash())
	fmt.Printf("[%s] 机器上线: %s | 名称: %s | 系统: %s | 地址: %s\n",
		time.Now().Format("15:04:05"), b.ID, b.NameOrDash(), b.OSOrDash(), b.Addr())
	prog.Info("bot %s 上线 %s 名称=%s 系统=%s", b.ID, b.Addr(), b.NameOrDash(), b.OSOrDash())

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
			// 远端终端的原始输出：原样写入 botlog（终端录像），并广播给所有订阅者
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			b.Log().WriteTranscript(p)
			b.PushOut(p)
		case protocol.TypeBye:
			m.Remove(b.ID, "客户端主动下线")
			return
		case protocol.TypeExecResult:
			// Agent/CLI 一次性命令的结果：投递给等待中的 CLI 桥接协程
			m.ResolveExec(&msg)
		}
	}
}
