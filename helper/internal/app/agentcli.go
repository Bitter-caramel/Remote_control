package app

// -cli 模式：给脚本/AI Agent 使用的机器可读命令行接口。
//
//	helper.exe -cli list [--json]
//	helper.exe -cli exec <botID> <命令...> [--timeout 秒] [--json]
//
// 发现顺序：--addr/--token 参数 → RA_ADDR/RA_TOKEN 环境变量 →
// logs/agent_endpoint.json（自动查找：当前目录、exe 同级及其 logs 子目录）。
//
// 退出码：本地调用错误=2；exec 时退出码透传远端命令的退出码（0=成功）。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/protocol"
)

type cliOpts struct {
	json    bool
	timeout int
	addr    string
	token   string
	pos     []string
}

func runCLIMode() int {
	args := os.Args[2:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: helper.exe -cli list [--json]")
		fmt.Fprintln(os.Stderr, "      helper.exe -cli exec <botID> <命令...> [--timeout 秒] [--json]")
		return 2
	}

	var o cliOpts
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json", "-json":
			o.json = true
		case "--timeout", "-timeout":
			if i+1 >= len(args) {
				return cliFail(o, "--timeout 缺少值")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return cliFail(o, "--timeout 必须是正整数")
			}
			o.timeout = n
			i++
		case "--addr", "-addr":
			if i+1 >= len(args) {
				return cliFail(o, "--addr 缺少值")
			}
			o.addr = args[i+1]
			i++
		case "--token", "-token":
			if i+1 >= len(args) {
				return cliFail(o, "--token 缺少值")
			}
			o.token = args[i+1]
			i++
		default:
			o.pos = append(o.pos, args[i])
		}
	}

	if o.addr == "" {
		o.addr = os.Getenv("RA_ADDR")
	}
	if o.token == "" {
		o.token = os.Getenv("RA_TOKEN")
	}
	if o.addr == "" || o.token == "" {
		addr, token, err := findEndpointFile()
		if err != nil {
			return cliFail(o, "找不到正在运行的 helper（logs/agent_endpoint.json 不存在）。请先启动 helper.exe，或用 --addr/--token 指定")
		}
		if o.addr == "" {
			o.addr = addr
		}
		if o.token == "" {
			o.token = token
		}
	}

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+o.addr+"/cli", nil)
	if err != nil {
		return cliFail(o, "无法连接本地 helper: "+err.Error())
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(&protocol.Message{Type: protocol.TypeCLIHello, Data: o.token}); err != nil {
		return cliFail(o, "握手发送失败: "+err.Error())
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack protocol.Message
	if err := conn.ReadJSON(&ack); err != nil || ack.Type != protocol.TypeCLIHelloAck || ack.Data != "ok" {
		reason := "无响应"
		if ack.Data != "" {
			reason = ack.Data
		}
		return cliFail(o, "鉴权失败: "+reason)
	}
	conn.SetReadDeadline(time.Time{})

	switch o.pos[0] {
	case "list":
		return cliList(conn, o)
	case "exec":
		return cliExec(conn, o)
	default:
		return cliFail(o, "未知子命令: "+o.pos[0])
	}
}

// cliFail 按纯文本或 JSON 输出本地错误，统一退出码 2
func cliFail(o cliOpts, reason string) int {
	if o.json {
		data, _ := json.Marshal(map[string]string{"error": reason})
		fmt.Println(string(data))
	} else {
		fmt.Fprintln(os.Stderr, "错误: "+reason)
	}
	return 2
}

func newMsgID() string {
	buf := make([]byte, 8)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func cliList(conn *websocket.Conn, o cliOpts) int {
	if err := conn.WriteJSON(&protocol.Message{Type: protocol.TypeCLIList}); err != nil {
		return cliFail(o, "查询失败: "+err.Error())
	}
	var resp protocol.Message
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&resp); err != nil {
		return cliFail(o, "读取列表失败: "+err.Error())
	}
	if resp.Type == protocol.TypeCLIError {
		return cliFail(o, resp.Data)
	}
	if resp.Type != protocol.TypeCLIListAck {
		return cliFail(o, "响应类型异常: "+resp.Type)
	}
	var bots []BotInfo
	if err := json.Unmarshal([]byte(resp.Data), &bots); err != nil {
		return cliFail(o, "解析列表失败: "+err.Error())
	}

	if o.json {
		out, _ := json.MarshalIndent(bots, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	if len(bots) == 0 {
		fmt.Println("（当前没有在线机器）")
		return 0
	}
	fmt.Println("botID        名称             系统                               地址                  状态")
	for _, b := range bots {
		name := b.Name
		if name == "" {
			name = "-"
		}
		osName := b.OS
		if osName == "" {
			osName = "-"
		}
		status := "在线"
		if b.Buffered {
			status = "心跳异常(等待中)"
		}
		fmt.Printf("%-12s %-16s %-34s %-21s %s\n",
			b.ID, truncRunes(name, 15), truncRunes(osName, 33), b.Addr, status)
	}
	return 0
}

func cliExec(conn *websocket.Conn, o cliOpts) int {
	if len(o.pos) < 3 {
		return cliFail(o, "用法: helper.exe -cli exec <botID> <命令...> [--timeout 秒] [--json]")
	}
	botID := o.pos[1]
	cmdLine := joinArgs(o.pos[2:])
	timeout := o.timeout
	if timeout <= 0 {
		timeout = protocol.DefaultExecTimeoutSec
	}
	if timeout > protocol.MaxExecTimeoutSec {
		timeout = protocol.MaxExecTimeoutSec
	}

	msgID := newMsgID()
	if err := conn.WriteJSON(&protocol.Message{
		Type:    protocol.TypeCLIExec,
		UserID:  botID,
		MsgID:   msgID,
		Data:    protocol.EncodeB64([]byte(cmdLine)),
		Timeout: timeout,
	}); err != nil {
		return cliFail(o, "发送失败: "+err.Error())
	}

	conn.SetReadDeadline(time.Now().Add(time.Duration(timeout+40) * time.Second))
	for {
		var resp protocol.Message
		if err := conn.ReadJSON(&resp); err != nil {
			return cliFail(o, "读取结果失败: "+err.Error())
		}
		if resp.MsgID != msgID {
			continue // 跳过不属于本请求的消息
		}
		switch resp.Type {
		case protocol.TypeCLIError:
			return cliFail(o, resp.Data)
		case protocol.TypeCLIResult:
			out, err := protocol.DecodeB64(resp.Data)
			if err != nil {
				return cliFail(o, "结果解码失败: "+err.Error())
			}
			text := string(out)
			if o.json {
				payload := struct {
					Bot      string `json:"bot"`
					ExitCode int    `json:"exit_code"`
					Output   string `json:"output"`
				}{botID, resp.ExitCode, text}
				data, _ := json.MarshalIndent(payload, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Print(text)
				if len(text) > 0 && text[len(text)-1] != '\n' {
					fmt.Println()
				}
			}
			if resp.ExitCode < 0 {
				return 2 // 超时/启动失败按本地错误处理
			}
			return resp.ExitCode
		}
	}
}

// joinArgs 把多个片段用空格拼回一条命令行。
// 约定：整条命令作为一个参数传入最可靠，例如
//
//	helper.exe -cli exec Bxxxx "ipconfig /all"
//	helper.exe -cli exec Bxxxx 'dir "C:\Program Files"'
//
// 这里不对片段自动加引号（cmd 对整条带引号命令的处理很反直觉），
// 命令内部需要的引号由调用者自己写。
func joinArgs(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
