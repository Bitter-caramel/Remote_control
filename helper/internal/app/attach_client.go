package app

// 终端窗口模式：helper.exe -attach <botID> -addr <127.0.0.1:port> -token <token>
//
// 由主面板 `bot [ID]` 通过 `cmd /c start` 在新 DOS 窗口中拉起。
// 它连接主程序的本地 IPC，把本窗口变成该 bot 的专属远程终端。
// 主窗口不被占用，可以继续管理其它机器、开更多终端窗口。

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/protocol"
)

// runAttachMode 终端窗口模式入口，返回进程退出码
func runAttachMode() int {
	enableUTF8Console()

	botID := flag.String("attach", "", "要接入的 botID")
	addr := flag.String("addr", "", "主程序本地 IPC 地址")
	token := flag.String("token", "", "接入令牌")
	flag.Parse()

	if *botID == "" || *addr == "" || *token == "" {
		waitBeforeExit("参数缺失（应以 helper.exe -attach <botID> -addr ... -token ... 方式启动）")
		return 1
	}

	// 1. 连接主程序本地 IPC 并鉴权
	u := "ws://" + *addr + "/attach"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		waitBeforeExit(fmt.Sprintf("无法连接协助主程序（主程序可能已退出）: %v", err))
		return 1
	}
	if err := conn.WriteJSON(&protocol.Message{
		Type:   protocol.TypeAttach,
		UserID: *botID,
		Data:   *token,
	}); err != nil {
		waitBeforeExit(fmt.Sprintf("接入请求发送失败: %v", err))
		return 1
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack protocol.Message
	if err := conn.ReadJSON(&ack); err != nil {
		waitBeforeExit(fmt.Sprintf("接入失败: %v", err))
		return 1
	}
	conn.SetReadDeadline(time.Time{})
	if ack.Type != protocol.TypeAttachAck || ack.Data != "ok" {
		waitBeforeExit(fmt.Sprintf("无法接入 %s：%s", *botID, ack.Data))
		return 1
	}

	// 2. 进入 raw 终端模式
	restore, raw, cols, rows := enterRawTerminal()
	_ = conn.WriteJSON(&protocol.Message{Type: protocol.TypeResize, Cols: cols, Rows: rows})

	// 标题 + 操作提示
	fmt.Printf("\r\n=== %s 的远程终端 ===  Ctrl+] 关闭本窗口（远端 shell 继续运行），Ctrl+C 发给远端\r\n\r\n", *botID)

	endReason := make(chan string, 1)

	if raw {
		// 输入协程：stdin 原始字节 → IPC（Ctrl+] 直接关闭本窗口）
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					chunk := buf[:n]
					if bytes.IndexByte(chunk, detachKey) >= 0 {
						restore()
						os.Exit(0) // 专用窗口，脱离即关窗
					}
					if werr := conn.WriteJSON(&protocol.Message{
						Type: protocol.TypeInput,
						Data: protocol.EncodeB64(chunk),
					}); werr != nil {
						endReason <- "与主程序的连接断开"
						return
					}
				}
				if err != nil {
					endReason <- "输入结束"
					return
				}
			}
		}()
	} else {
		// 降级行模式（stdin 不是控制台）
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				line := sc.Text()
				if line == "/quit" {
					endReason <- "已退出"
					return
				}
				if err := conn.WriteJSON(&protocol.Message{
					Type: protocol.TypeInput,
					Data: protocol.EncodeB64([]byte(line + "\r\n")),
				}); err != nil {
					endReason <- "与主程序的连接断开"
					return
				}
			}
			endReason <- "输入结束"
		}()
	}

	// 3. 主循环：远端输出 → 本窗口
	reason := ""
	for {
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			reason = "与主程序的连接断开"
			break
		}
		switch msg.Type {
		case protocol.TypeOutput:
			p, err := protocol.DecodeB64(msg.Data)
			if err != nil {
				continue
			}
			os.Stdout.Write(p)
		case protocol.TypeBye:
			reason = "该机器已下线"
		}
		if reason != "" {
			break
		}
	}

	restore()
	waitBeforeExit(reason)
	return 0
}

// waitBeforeExit 打印结束原因并等待回车，避免新窗口一闪而过看不到信息
func waitBeforeExit(msg string) {
	fmt.Print("\r\n" + msg + "\r\n按回车键关闭窗口...")
	r := bufio.NewReader(os.Stdin)
	_, _ = r.ReadString('\n')
}
