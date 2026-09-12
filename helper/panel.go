package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"remoteassist-helper/logx"
)

// RunPanel 前台 DOS 控制面板：像 codex 的 cli 面板一样，只接收既定的指令和参数；
// 其余所有程序（监听、心跳检测、终端窗口桥接）后台运行，运行过程生成日志。
func RunPanel(in io.Reader, out io.Writer, m *Manager, prog *logx.ProgramLog, botslog *logx.BotsLog, attachSrv *AttachServer) {
	sc := bufio.NewScanner(in) // 全程共用一个 Scanner，内嵌终端降级模式也从这里读
	for {
		fmt.Fprint(out, "assist> ")
		if !sc.Scan() {
			// 标准输入被关闭（例如作为后台服务运行），面板不可用，程序保持后台运行
			fmt.Fprintln(out, "(输入已关闭，程序转入纯后台运行；通过任务管理器结束进程可退出)")
			select {}
		}
		line := strings.TrimSpace(sc.Text())
		args := strings.Fields(line)
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "help":
			printHelp(out)
		case "log":
			handleLog(out, args[1:], m, prog, botslog)
		case "bots":
			handleBots(out, args[1:], m, botslog)
		case "bot":
			if len(args) < 2 {
				fmt.Fprintln(out, "用法: bot [botID]")
				continue
			}
			handleBot(out, sc, m, attachSrv, args[1])
		case "exit":
			// 退出程序：不再监听端口，不再执行服务程序，直至下次启动
			prog.Info("操作者执行 exit，程序退出")
			fmt.Fprintln(out, "程序已退出。")
			return
		default:
			fmt.Fprintf(out, "未知命令: %s （输入 help 查看可用命令）\n", args[0])
		}
	}
}

// handleBot 为指定 bot 打开专属终端窗口；主面板不被阻塞，可继续管理其它机器。
// 不支持弹窗的平台（或弹窗失败）降级为内嵌终端。
func handleBot(out io.Writer, sc *bufio.Scanner, m *Manager, attachSrv *AttachServer, id string) {
	b := m.Get(id)
	if b == nil {
		fmt.Fprintf(out, "bot %s 不在线或不存在（用 bots 命令查看在线列表）\n", id)
		return
	}
	if b.HasAttach() {
		fmt.Fprintf(out, "bot %s 的终端窗口已经打开了（同一机器同时只能开一个窗口，请先关闭原窗口）\n", id)
		return
	}
	if err := openAttachWindow(id, attachSrv.Addr(), attachSrv.Token()); err != nil {
		fmt.Fprintf(out, "（无法弹出新窗口：%v，改为在当前窗口内嵌打开）\n", err)
		runBotPanel(out, sc, m, id)
		return
	}
	fmt.Fprintf(out, "已在新窗口打开 %s 的终端，本窗口可继续执行其它命令\n", id)
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `
可用命令:
  log -p            查看程序运行日志 (programlog)
  log --bots        查看 botslog（所有连接过的主机）
  log --botlog+ID   查看指定 botID 的专属日志
  bots              查看当前已连接机器的基础信息
  bots -a           查看连接过的全部机器（读 botslog）
  bots -l           查看当前机器的详细信息
  bot [botID]       为该机器弹出独立终端窗口（可同时管理多台；窗口内 Ctrl+] 关闭）
  exit              退出程序
`)
}

// handleLog 处理 log 命令族
func handleLog(out io.Writer, args []string, m *Manager, prog *logx.ProgramLog, botslog *logx.BotsLog) {
	if len(args) == 0 {
		fmt.Fprintln(out, "用法: log -p | log --bots | log --botlog+ID")
		return
	}
	switch a := args[0]; {
	case a == "-p":
		content, err := prog.Read()
		if err != nil {
			fmt.Fprintln(out, err)
			return
		}
		fmt.Fprintln(out, content)
	case a == "--bots":
		content, err := botslog.Read()
		if err != nil {
			fmt.Fprintln(out, err)
			return
		}
		fmt.Fprintln(out, content)
	case strings.HasPrefix(a, "--botlog"):
		// 支持 log --botlog+ID / log --botlog ID 两种写法
		id := strings.TrimLeft(strings.TrimPrefix(a, "--botlog"), "+ ")
		if id == "" && len(args) > 1 {
			id = args[1]
		}
		if id == "" {
			fmt.Fprintln(out, "用法: log --botlog+ID")
			return
		}
		content, err := logx.ReadBotLog(m.logDir, id)
		if err != nil {
			fmt.Fprintln(out, err)
			return
		}
		fmt.Fprintln(out, content)
	default:
		fmt.Fprintln(out, "未知参数:", a)
	}
}

// handleBots 处理 bots 命令族
func handleBots(out io.Writer, args []string, m *Manager, botslog *logx.BotsLog) {
	if len(args) == 0 {
		// bots：查看已连接机器的基础信息
		list := m.Snapshot()
		if len(list) == 0 {
			fmt.Fprintln(out, "(当前没有机器在线)")
			return
		}
		fmt.Fprintf(out, "%-12s %-21s\n", "BOTID", "地址")
		for _, b := range list {
			fmt.Fprintf(out, "%-12s %-21s\n", b.ID, b.Addr())
		}
		return
	}
	switch args[0] {
	case "-a":
		// 查看连接过的全部机器（读 botslog）
		content, err := botslog.Read()
		if err != nil {
			fmt.Fprintln(out, err)
			return
		}
		fmt.Fprint(out, content)
	case "-l":
		// 查看详细信息
		list := m.Snapshot()
		if len(list) == 0 {
			fmt.Fprintln(out, "(当前没有机器在线)")
			return
		}
		for _, b := range list {
			status := "在线"
			if b.Buffered {
				status = fmt.Sprintf("心跳丢失 %d 个周期，缓存区等待中", b.Missed)
			}
			fmt.Fprintf(out, "BOTID      : %s\n", b.ID)
			fmt.Fprintf(out, "地址       : %s\n", b.Addr())
			fmt.Fprintf(out, "上线时间   : %s\n", b.OnlineAt.Format("2006-01-02 15:04:05"))
			fmt.Fprintf(out, "最近心跳   : %s\n", time.Unix(0, b.LastBeat.Load()).Format("2006-01-02 15:04:05"))
			fmt.Fprintf(out, "状态       : %s\n", status)
			fmt.Fprintf(out, "botlog     : botlogs/%s.log\n", b.ID)
			fmt.Fprintln(out, strings.Repeat("-", 40))
		}
	default:
		fmt.Fprintln(out, "未知参数:", args[0])
	}
}
