package app

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"remoteassist-helper/protocol"
)

// detachKey 终端面板脱离键：Ctrl+]（0x1d，与 telnet 一致）。
// 它不会发送给远端；Ctrl+C（0x03）则会原样透传，用来中断远端进程。
const detachKey byte = 0x1d

// runBotPanel bot [botID]：接入该机器的交互式终端。
//
// 真实控制台下进入 raw 模式：按键（含 Ctrl+C、方向键、Tab）实时透传，
// 远端输出（含颜色/光标控制）实时渲染，当前目录、环境变量、交互程序全部保持。
// stdin 不是控制台（管道重定向/非 Windows/自动化测试）时，降级为按行发送。
//
// 输入 Ctrl+] 返回主面板；返回后远端 shell 继续存活，下次进入仍是原现场。
func runBotPanel(out io.Writer, sc *bufio.Scanner, m *Manager, id string) {
	b := m.Get(id)
	if b == nil {
		fmt.Fprintf(out, "bot %s 不在线或不存在（用 bots 命令查看在线列表）\n", id)
		return
	}
	if !b.TryAttach() {
		fmt.Fprintf(out, "bot %s 已有终端窗口打开，请先关闭原窗口\n", id)
		return
	}
	defer b.ReleaseAttach()

	restore, raw, cols, rows := enterRawTerminal()
	defer restore()

	// 通知远端终端尺寸，并清掉积压输出
	_ = b.Send(&protocol.Message{Type: protocol.TypeResize, Cols: cols, Rows: rows})
	b.DiscardOut()

	var stopped atomic.Bool
	detach := make(chan struct{})
	offline := make(chan struct{})

	finish := func() {
		if stopped.CompareAndSwap(false, true) {
			close(detach)
		}
	}

	// 后台：机器下线时自动退出终端
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-detach:
				return
			case <-ticker.C:
				if m.Get(id) == nil {
					close(offline)
					return
				}
			}
		}
	}()

	if raw {
		runRawTerminal(b, finish, detach, offline)
	} else {
		runCookedTerminal(out, sc, b, finish, detach, offline)
	}

	b.blog.Sys("终端面板脱离")
	fmt.Fprintf(out, "\r\n[已返回主面板，远端 shell 仍在运行；再次 bot %s 可回到原现场]\r\n", id)
}

// runRawTerminal 真实控制台：原始字节透传
func runRawTerminal(b *BOT, finish func(), detach, offline <-chan struct{}) {
	// 输入协程：stdin 原始字节 → 远端（Ctrl+] 除外）
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if bytes.IndexByte(chunk, detachKey) >= 0 {
					finish()
					return
				}
				if err := b.Send(&protocol.Message{
					Type: protocol.TypeInput,
					Data: protocol.EncodeB64(chunk),
				}); err != nil {
					finish()
					return
				}
			}
			if err != nil {
				finish()
				return
			}
		}
	}()

	fmt.Printf("\r\n--- 已接入 %s 的终端（Ctrl+] 返回主面板，Ctrl+C 发送给远端）---\r\n", b.ID)
	for {
		select {
		case p := <-b.OutC:
			os.Stdout.Write(p)
		case <-offline:
			fmt.Print("\r\n[该机器已下线]\r\n")
			return
		case <-detach:
			return
		}
	}
}

// runCookedTerminal 降级模式（stdin 是管道）：按行读取，自动补回车。
// 脱离方式：单独一行只含 Ctrl+] 字符，或输入 /quit。
func runCookedTerminal(out io.Writer, sc *bufio.Scanner, b *BOT, finish func(), detach, offline <-chan struct{}) {
	// 输出协程：远端输出 → 本地面板
	go func() {
		for {
			select {
			case <-detach:
				return
			case p, ok := <-b.OutC:
				if !ok {
					return
				}
				fmt.Fprint(out, string(p))
			}
		}
	}()

	fmt.Fprintf(out, "--- 已接入 %s 的终端（行模式；输入 /quit 返回主面板）---\n", b.ID)
	for sc.Scan() {
		line := sc.Text()
		if line == "/quit" || line == string(detachKey) {
			finish()
			break
		}
		select {
		case <-offline:
			fmt.Fprintln(out, "[该机器已下线]")
			return
		default:
		}
		if err := b.Send(&protocol.Message{
			Type: protocol.TypeInput,
			Data: protocol.EncodeB64([]byte(strings.TrimRight(line, "\r\n") + "\r\n")),
		}); err != nil {
			fmt.Fprintf(out, "发送失败（机器可能已掉线）: %v\n", err)
			finish()
			break
		}
		time.Sleep(100 * time.Millisecond) // 给 shell 一点回显时间
	}
}
