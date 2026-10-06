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

	// 段累积缓冲：key = ctxID，value = 该段自上一段结束以来的原始输出。
	// 段结束时落库，供重连（helper 重启 / bot 重启）后回放终端画面；
	// 只会被本协程访问，无需加锁。本机保留上下文（local）不在库里，跳过。
	segBuf := make(map[string][]byte)

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
			// 远端某条操作上下文的原始输出：原样写入 botlog（终端录像），
			// 并按 CtxID 路由给该上下文的订阅者（owner/operate 实时，watch 按段）
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			b.Log().WriteTranscript(p)
			b.PushCtxOut(msg.CtxID, p)
			if msg.CtxID != core.LocalCtxID {
				segBuf[msg.CtxID] = appendSegment(segBuf[msg.CtxID], p)
			}
		case protocol.TypeCtxSegEnd:
			// 一段操作结束：落库当时的 cwd（重连恢复现场）与整段输出（命令历史回放），
			// 并把该段推给观看者
			cwd := ""
			if raw, err := protocol.DecodeB64(msg.Data); err == nil {
				cwd = string(raw)
			}
			if cwd != "" {
				_ = st.UpdateCwd(msg.CtxID, cwd)
			}
			if msg.CtxID != core.LocalCtxID {
				data := segBuf[msg.CtxID]
				delete(segBuf, msg.CtxID)
				if err := st.AppendSegment(msg.CtxID, msg.Seq, cwd, data); err != nil {
					prog.Error("落库上下文 %s 第 %d 段失败: %v", msg.CtxID, msg.Seq, err)
				}
			}
			b.PushCtxSegEnd(msg.CtxID, msg.Seq)
		case protocol.TypeCtxOpened:
			// 上下文就绪（Err 非空表示开启失败）：转发给等待中的订阅者
			b.NotifyCtxOpened(msg.CtxID, msg.Err)
		case protocol.TypeCtxClosed:
			// 该上下文的 shell 已终止：销毁内存态并断开订阅者
			b.DropCtx(msg.CtxID, "shell 已退出")
		case protocol.TypeBye:
			m.Remove(b.ID, "客户端主动下线")
			return
		case protocol.TypeExecResult:
			// Agent/CLI 一次性命令的结果：投递给等待中的 CLI 桥接协程
			m.ResolveExec(&msg)
		}
	}
}

// appendSegment 把新输出追加到段缓冲；超过单段上限时只保留末尾，
// 与落库时的截断口径一致，避免超长输出在段结束前把内存撑大。
func appendSegment(buf, p []byte) []byte {
	buf = append(buf, p...)
	if len(buf) > store.SegmentLimit {
		buf = append([]byte(nil), buf[len(buf)-store.SegmentLimit:]...)
	}
	return buf
}
