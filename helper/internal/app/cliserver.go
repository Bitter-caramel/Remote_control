package app

// 本地 IPC 的 /cli 端点：供 `helper.exe -cli`（以及 AI Agent 脚本化调用）使用。
// 与 /attach（人工终端窗口）的区别：
//   - 请求/响应式，一次一条命令，返回完整输出和退出码，不占终端窗口位；
//   - 命令在用户端以独立的一次性 shell 执行，与交互 PTY 互不干扰；
//   - 同一令牌鉴权，只监听 127.0.0.1。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/protocol"
)

// endpointFile 落盘的本机接入点信息（供 CLI 自动发现）
type endpointFile struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

const endpointFileName = "agent_endpoint.json"

// writeEndpointFile 把接入点写入 <logDir>/agent_endpoint.json；
// 失败不致命（CLI 仍可用 -addr/-token 或环境变量显式指定），返回空串。
func writeEndpointFile(logDir, addr, token string) string {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return ""
	}
	p := filepath.Join(logDir, endpointFileName)
	data, _ := json.MarshalIndent(endpointFile{Addr: addr, Token: token, PID: os.Getpid()}, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return ""
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return ""
	}
	return p
}

// findEndpointFile 在常见位置查找接入点文件。
// 从两个起点（当前工作目录、exe 所在目录）各自向上最多 4 级查找，
// 每级检查：<dir>/agent_endpoint.json、<dir>/logs/agent_endpoint.json；
// 另外兼容本仓库布局（bin/helper.exe + helper/logs/…）：<dir>/helper/logs/…。
func findEndpointFile() (addr, token string, err error) {
	var roots []string
	if cwd, e := os.Getwd(); e == nil {
		roots = append(roots, cwd)
	}
	if exe, e := os.Executable(); e == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	var dirs []string
	for _, r := range roots {
		d := r
		for i := 0; i < 5 && d != ""; i++ {
			dirs = append(dirs,
				filepath.Join(d, "logs"),
				filepath.Join(d, "helper", "logs"), // 仓库布局：bin/ 与 helper/ 同级
			)
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	for _, d := range dirs {
		data, e := os.ReadFile(filepath.Join(d, endpointFileName))
		if e != nil {
			continue
		}
		var ep endpointFile
		if json.Unmarshal(data, &ep) == nil && ep.Addr != "" && ep.Token != "" {
			return ep.Addr, ep.Token, nil
		}
	}
	return "", "", os.ErrNotExist
}

// handleCLI 处理一个 CLI 连接的完整生命周期
func (a *AttachServer) handleCLI(up websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 1. 5 秒内完成令牌握手
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var hello protocol.Message
		if err := conn.ReadJSON(&hello); err != nil ||
			hello.Type != protocol.TypeCLIHello || hello.Data != a.token {
			conn.WriteJSON(protocol.Message{Type: protocol.TypeCLIHelloAck, Data: "鉴权失败"})
			return
		}
		conn.SetReadDeadline(time.Time{})
		if err := conn.WriteJSON(protocol.Message{Type: protocol.TypeCLIHelloAck, Data: "ok"}); err != nil {
			return
		}

		var wmu sync.Mutex
		send := func(msg *protocol.Message) {
			wmu.Lock()
			conn.WriteJSON(msg)
			wmu.Unlock()
		}

		// 2. 请求循环：list 立即应答；exec 起独立协程桥接，支持并发多条
		for {
			var req protocol.Message
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			switch req.Type {
			case protocol.TypeCLIList:
				data, _ := json.Marshal(a.m.BotInfos())
				send(&protocol.Message{Type: protocol.TypeCLIListAck, Data: string(data)})
			case protocol.TypeCLIExec:
				go a.bridgeCLIExec(send, req)
			default:
				send(&protocol.Message{Type: protocol.TypeCLIError, MsgID: req.MsgID, Data: "未知请求类型"})
			}
		}
	}
}

// bridgeCLIExec 把一条 CLI exec 请求转成远程 exec，并等待结果回包
func (a *AttachServer) bridgeCLIExec(send func(*protocol.Message), req protocol.Message) {
	fail := func(reason string) {
		send(&protocol.Message{Type: protocol.TypeCLIError, MsgID: req.MsgID, Data: reason})
	}

	cmdBytes, err := protocol.DecodeB64(req.Data)
	if err != nil || len(cmdBytes) == 0 {
		fail("命令为空或编码异常")
		return
	}
	cmdLine := string(cmdBytes)

	b := a.m.Get(req.UserID)
	if b == nil {
		fail("机器不在线: " + req.UserID)
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = protocol.DefaultExecTimeoutSec
	}
	if timeout > protocol.MaxExecTimeoutSec {
		timeout = protocol.MaxExecTimeoutSec
	}

	msgID := req.MsgID
	if msgID == "" {
		buf := make([]byte, 8)
		rand.Read(buf)
		msgID = hex.EncodeToString(buf)
	}

	ch := make(chan *protocol.Message, 1)
	if !a.m.RegisterExec(msgID, b.ID, ch) {
		fail("MsgID 冲突，请重试")
		return
	}
	defer a.m.UnregisterExec(msgID)

	b.blog.AgentCmd(cmdLine)
	if err := b.Send(&protocol.Message{
		Type:    protocol.TypeExec,
		MsgID:   msgID,
		Data:    protocol.EncodeB64(cmdBytes),
		Timeout: timeout,
	}); err != nil {
		b.blog.AgentOut("发送失败: "+err.Error(), -1)
		fail("发送到机器失败: " + err.Error())
		return
	}

	select {
	case res := <-ch:
		if res == nil {
			b.blog.AgentOut("机器在执行期间下线", -1)
			fail("机器在执行期间下线")
			return
		}
		b.blog.AgentOut(mustB64String(res.Data), res.ExitCode)
		send(&protocol.Message{
			Type:     protocol.TypeCLIResult,
			MsgID:    msgID,
			Data:     res.Data, // exec_result 的 Data 已是输出的 base64，原样透传
			ExitCode: res.ExitCode,
		})
	case <-time.After(time.Duration(timeout+20) * time.Second):
		b.blog.AgentOut("服务端等待超时", -1)
		fail("等待执行结果超时（远端命令可能仍在运行）")
	}
}

// mustB64String 尽力把 base64 解成文本（仅用于写日志，失败原样返回）
func mustB64String(s string) string {
	if p, err := protocol.DecodeB64(s); err == nil {
		return string(p)
	}
	return s
}
