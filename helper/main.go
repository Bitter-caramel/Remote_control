package main

import (
	"flag"
	"fmt"
	"os"

	"remoteassist-helper/logx"
)

// 服务端固定配置
const (
	ListenAddr = ":8080" // 对外监听端口
	LogDir     = "logs"  // 日志目录（program.log / bots.log / botlogs/<botID>.log）
)

func main() {
	// 终端窗口模式（由主面板 `bot [ID]` 弹新窗口拉起），提前拦截 flag，
	// 不初始化任何监听和日志
	if len(os.Args) >= 2 && os.Args[1] == "-attach" {
		os.Exit(runAttachMode())
	}
	flag.Parse()

	// 控制台切到 UTF-8，保证 bot 回显中的中文在传统 cmd 窗口也能正常显示
	enableUTF8Console()

	// 初始化三类日志
	prog, err := logx.NewProgramLog(LogDir)
	if err != nil {
		fmt.Println("初始化 programlog 失败:", err)
		return
	}
	defer prog.Close()
	botslog := logx.NewBotsLog(LogDir)

	// bots 管理器（bots 列表 + 缓存区列表）
	m := NewManager(LogDir, prog, botslog)

	// 本地 IPC：`bot [ID]` 弹出的终端窗口通过它桥接到对应 bot
	attachSrv, err := NewAttachServer(m)
	if err != nil {
		prog.Error("启动本地终端接入服务失败: %v", err)
		fmt.Println("启动本地终端接入服务失败:", err)
		return
	}
	defer attachSrv.Close()

	// 后台：心跳检测
	StartHeartbeatChecker(m, prog)

	// 后台：监听端口，接收用户端连接（TCP → WebSocket）
	go func() {
		if err := StartListener(ListenAddr, m, prog); err != nil {
			prog.Error("监听服务异常退出: %v", err)
			fmt.Println("监听服务异常退出:", err)
			os.Exit(1)
		}
	}()

	// 前台：DOS 控制面板（阻塞，直到输入 exit）
	RunPanel(os.Stdin, os.Stdout, m, prog, botslog, attachSrv)
}
