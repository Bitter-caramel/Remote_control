package app

// 端到端回归测试：
// 注册 → 本地 IPC 终端窗口桥接（输入/输出/鉴权/单机单窗/下线通知）→ bye/心跳销毁。

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// newTestServer 用 httptest 起一个真实的远程 WebSocket 服务（user 端连入侧）
func newTestServer(t *testing.T, m *Manager, prog *logx.ProgramLog) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(wsHandler(m, prog))
	t.Cleanup(ts.Close)
	return ts
}

// fakeClient 模拟用户端：注册后把收到的终端输入回显为输出（模拟 shell）
func fakeClient(t *testing.T, url, userID string) (*websocket.Conn, string) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	if err := conn.WriteJSON(protocol.Message{
		Type:   protocol.TypeRegister,
		UserID: userID,
		Name:   "fake-" + userID,
		OS:     "test/os (1.0)",
	}); err != nil {
		t.Fatalf("发送注册失败: %v", err)
	}
	var ack protocol.Message
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("等待注册应答失败: %v", err)
	}
	if ack.Type != protocol.TypeRegisterAck || ack.UserID == "" {
		t.Fatalf("注册应答异常: %+v", ack)
	}
	go func() {
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
				resp := append(append([]byte{}, p...), []byte("E2E-RESPONSE\r\n")...)
				_ = conn.WriteJSON(protocol.Message{
					Type: protocol.TypeOutput,
					Data: protocol.EncodeB64(resp),
				})
			case protocol.TypeExec:
				p, err := protocol.DecodeB64(msg.Data)
				if err != nil {
					continue
				}
				resp := append(append([]byte{}, p...), []byte(" EXEC-OK")...)
				_ = conn.WriteJSON(protocol.Message{
					Type:     protocol.TypeExecResult,
					MsgID:    msg.MsgID,
					Data:     protocol.EncodeB64(resp),
					ExitCode: 7,
				})
			}
		}
	}()
	return conn, ack.UserID
}

// attachDial 模拟一个弹出的终端窗口，连本地 IPC 完成接入握手
func attachDial(t *testing.T, asrv *AttachServer, botID, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+asrv.Addr()+"/attach", nil)
	if err != nil {
		t.Fatalf("IPC 拨号失败: %v", err)
	}
	if err := conn.WriteJSON(protocol.Message{
		Type: protocol.TypeAttach, UserID: botID, Data: token,
	}); err != nil {
		t.Fatalf("发送接入请求失败: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack protocol.Message
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("等待接入应答失败: %v", err)
	}
	conn.SetReadDeadline(time.Time{})
	if ack.Type != protocol.TypeAttachAck {
		t.Fatalf("接入应答类型异常: %+v", ack)
	}
	if ack.Data != "ok" {
		t.Fatalf("接入被拒: %s", ack.Data)
	}
	return conn
}

func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	prog, err := logx.NewProgramLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prog.Close() })
	botslog := logx.NewBotsLog(dir)
	m := NewManager(dir, prog, botslog)

	// 远程接入侧
	ts := newTestServer(t, m, prog)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// 本地终端窗口 IPC
	asrv, err := NewAttachServer(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(asrv.Close)

	conn1, id1 := fakeClient(t, url, "")
	if !strings.HasPrefix(id1, "B") {
		t.Fatalf("分配的 botID 格式异常: %s", id1)
	}
	conn2, id2 := fakeClient(t, url, "Btest0001")
	defer conn1.Close()
	defer conn2.Close()

	// --- 鉴权失败：错误令牌 ---
	bad, _, err := websocket.DefaultDialer.Dial("ws://"+asrv.Addr()+"/attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	bad.WriteJSON(protocol.Message{Type: protocol.TypeAttach, UserID: id2, Data: "wrong-token"})
	bad.SetReadDeadline(time.Now().Add(3 * time.Second))
	var badAck protocol.Message
	bad.ReadJSON(&badAck)
	if badAck.Data == "ok" {
		t.Error("错误令牌不应接入成功")
	}
	bad.Close()

	// --- 正常接入 + 输入输出桥接 ---
	term := attachDial(t, asrv, id2, asrv.Token())
	defer term.Close()
	term.WriteJSON(protocol.Message{Type: protocol.TypeInput, Data: protocol.EncodeB64([]byte("whoami\r\n"))})

	// 读取桥接输出，直到看到模拟响应（gorilla 读超时后连接不可恢复，只设一次总 deadline）
	term.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := ""
	for {
		var msg protocol.Message
		if err := term.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == protocol.TypeOutput {
			p, _ := protocol.DecodeB64(msg.Data)
			got += string(p)
			if strings.Contains(got, "E2E-RESPONSE") {
				break
			}
		}
	}
	if !strings.Contains(got, "whoami") || !strings.Contains(got, "E2E-RESPONSE") {
		t.Fatalf("终端桥接输出异常: %q", got)
	}

	// --- 同一 bot 第二个窗口应被拒绝 ---
	dup, _, _ := websocket.DefaultDialer.Dial("ws://"+asrv.Addr()+"/attach", nil)
	dup.WriteJSON(protocol.Message{Type: protocol.TypeAttach, UserID: id2, Data: asrv.Token()})
	dup.SetReadDeadline(time.Now().Add(3 * time.Second))
	var dupAck protocol.Message
	dup.ReadJSON(&dupAck)
	if dupAck.Data == "ok" {
		t.Error("同一机器不应允许两个终端窗口同时接入")
	}
	dup.Close()

	// --- botlog 终端录像包含桥接内容 ---
	botlogContent, err := logx.ReadBotLog(dir, id2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(botlogContent, "whoami") || !strings.Contains(botlogContent, "E2E-RESPONSE") {
		t.Errorf("botlog 终端录像缺少内容:\n%s", botlogContent)
	}

	// --- 机器下线 → 终端窗口收到 bye（ticker 周期 1s，给 4s 总时限）---
	_ = conn2.WriteJSON(protocol.Message{Type: protocol.TypeBye})
	term.SetReadDeadline(time.Now().Add(4 * time.Second))
	gotBye := false
	for {
		var msg protocol.Message
		if err := term.ReadJSON(&msg); err != nil {
			break
		}
		t.Logf("bye 阶段收到消息: %+v", msg)
		if msg.Type == protocol.TypeBye {
			gotBye = true
			break
		}
	}
	if !gotBye {
		t.Error("机器下线后终端窗口应收到 bye 通知")
	}

	// 面板基本命令仍然正常（bot 不再走内嵌路径）
	script := "bots\nlog --bots\nexit\n"
	var out bytes.Buffer
	RunPanel(strings.NewReader(script), &out, m, prog, botslog, asrv)
	console := out.String()
	if !strings.Contains(console, id1) {
		t.Errorf("bots 输出缺少在线机器:\n%s", console)
	}

	// 心跳销毁路径：3 个周期无心跳 → 下线
	b := m.Get(id1)
	if b == nil {
		t.Fatal("bot 应在线")
	}
	m.Miss(b)
	if !b.Buffered || m.Get(id1) == nil {
		t.Error("第 1 次丢失后应进入缓存区且仍在 bots 列表")
	}
	m.Miss(b)
	m.Miss(b)
	if m.Get(id1) != nil {
		t.Error("3 个周期无心跳后 bot 应已下线")
	}
}

// cliDial 模拟 helper.exe -cli：连 /cli 完成令牌握手
func cliDial(t *testing.T, asrv *AttachServer, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+asrv.Addr()+"/cli", nil)
	if err != nil {
		t.Fatalf("CLI 拨号失败: %v", err)
	}
	conn.WriteJSON(protocol.Message{Type: protocol.TypeCLIHello, Data: token})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ack protocol.Message
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("CLI 握手应答失败: %v", err)
	}
	if ack.Type != protocol.TypeCLIHelloAck || ack.Data != "ok" {
		t.Fatalf("CLI 握手被拒: %+v", ack)
	}
	conn.SetReadDeadline(time.Time{})
	return conn
}

func TestCLIFlow(t *testing.T) {
	dir := t.TempDir()
	prog, _ := logx.NewProgramLog(dir)
	t.Cleanup(func() { _ = prog.Close() })
	m := NewManager(dir, prog, logx.NewBotsLog(dir))
	ts := newTestServer(t, m, prog)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	asrv, err := NewAttachServer(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(asrv.Close)

	conn, id := fakeClient(t, url, "Bclitest1")
	defer conn.Close()

	// 发现文件已写入
	if data, err := os.ReadFile(filepath.Join(dir, endpointFileName)); err != nil ||
		!strings.Contains(string(data), asrv.Addr()) {
		t.Errorf("endpoint 发现文件异常: %v %s", err, data)
	}

	// --- 错误令牌拒绝 ---
	bad, _, _ := websocket.DefaultDialer.Dial("ws://"+asrv.Addr()+"/cli", nil)
	bad.WriteJSON(protocol.Message{Type: protocol.TypeCLIHello, Data: "nope"})
	bad.SetReadDeadline(time.Now().Add(3 * time.Second))
	var badAck protocol.Message
	bad.ReadJSON(&badAck)
	if badAck.Data == "ok" {
		t.Error("CLI 错误令牌不应通过")
	}
	bad.Close()

	c := cliDial(t, asrv, asrv.Token())
	defer c.Close()

	// --- list ---
	c.WriteJSON(protocol.Message{Type: protocol.TypeCLIList})
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var list protocol.Message
	if err := c.ReadJSON(&list); err != nil || list.Type != protocol.TypeCLIListAck {
		t.Fatalf("list 应答异常: %+v err=%v", list, err)
	}
	if !strings.Contains(list.Data, id) {
		t.Errorf("list 结果缺少 bot: %s", list.Data)
	}
	var infos []BotInfo
	if json.Unmarshal([]byte(list.Data), &infos) != nil || len(infos) != 1 || infos[0].ID != id {
		t.Errorf("list JSON 解析异常: %s", list.Data)
	}
	if infos[0].Name != "fake-"+id || infos[0].OS != "test/os (1.0)" {
		t.Errorf("list 缺少 name/os 透传: %+v", infos[0])
	}

	// --- exec 全链路：输出与退出码 ---
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	c.WriteJSON(protocol.Message{
		Type:    protocol.TypeCLIExec,
		UserID:  id,
		MsgID:   "m-1",
		Data:    protocol.EncodeB64([]byte("whoami")),
		Timeout: 10,
	})
	var res protocol.Message
	if err := c.ReadJSON(&res); err != nil {
		t.Fatalf("exec 应答失败: %v", err)
	}
	if res.Type != protocol.TypeCLIResult || res.MsgID != "m-1" || res.ExitCode != 7 {
		t.Fatalf("exec 结果异常: %+v", res)
	}
	out, _ := protocol.DecodeB64(res.Data)
	if string(out) != "whoami EXEC-OK" {
		t.Fatalf("exec 输出异常: %q", out)
	}

	// --- exec 不存在的 bot → 立即错误 ---
	c.WriteJSON(protocol.Message{
		Type:   protocol.TypeCLIExec,
		UserID: "Bmissing",
		MsgID:  "m-2",
		Data:   protocol.EncodeB64([]byte("ver")),
	})
	var errMsg protocol.Message
	if err := c.ReadJSON(&errMsg); err != nil || errMsg.Type != protocol.TypeCLIError {
		t.Fatalf("离线 bot exec 应返回错误: %+v err=%v", errMsg, err)
	}

	// --- botlog 记录了 Agent 审计 ---
	bl, err := logx.ReadBotLog(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bl, "[AGENT] CMD > whoami") ||
		!strings.Contains(bl, "[AGENT] OUT < exit=7 whoami EXEC-OK") {
		t.Errorf("botlog 缺少 Agent 审计:\n%s", bl)
	}
}
