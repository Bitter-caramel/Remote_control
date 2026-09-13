package app

import (
	"bufio"
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

// Run 协助者服务端主入口（由 cmd/helper 调用）
func Run() {
	// 终端窗口模式（由主面板 `bot [ID]` 弹新窗口拉起），提前拦截 flag，
	// 不初始化任何监听和日志
	if len(os.Args) >= 2 && os.Args[1] == "-attach" {
		os.Exit(runAttachMode())
	}
	// CLI/Agent 模式：脚本化查询/执行，连接到正在运行的主程序
	if len(os.Args) >= 2 && os.Args[1] == "-cli" {
		os.Exit(runCLIMode())
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

	// 本地 IPC：终端窗口与 CLI/Agent 通过它桥接到对应 bot
	attachSrv, err := NewAttachServer(m, LogDir)
	if err != nil {
		prog.Error("启动本地终端接入服务失败: %v", err)
		fmt.Println("启动本地终端接入服务失败:", err)
		return
	}
	defer attachSrv.Close()

	// 后台：心跳检测
	StartHeartbeatChecker(m, prog)

	// 后台：监听端口，接收用户端连接（TCP → WebSocket）
	// 可用环境变量 RA_LISTEN 覆盖监听地址，如 :18080
	listenAddr := ListenAddr
	if v := os.Getenv("RA_LISTEN"); v != "" {
		listenAddr = v
	}
	ln, err := NewListener(listenAddr)
	if err != nil {
		prog.Error("监听端口 %s 绑定失败: %v", listenAddr, err)
		fatalWait(`无法在 %s 启动监听：%v
可能原因：该端口已被其他程序占用（本机已有另一个 helper，或其它程序占用了 8080）。
解决办法（任选其一）：
  1) 关闭占用端口的程序（任务管理器查看，或命令：netstat -ano | findstr :8080）；
  2) 换端口启动：先设置环境变量再启动，例如 PowerShell 执行
        $env:RA_LISTEN=':18080'; .\helper.exe
     并把 user 端 config.json 的 server_addr 改成对应地址（如 127.0.0.1:18080）。`, listenAddr, err)
		return
	}
	go ServeListener(ln, m, prog)

	// 前台：DOS 控制面板（阻塞，直到输入 exit）
	RunPanel(os.Stdin, os.Stdout, m, prog, botslog, attachSrv)
}

// fatalWait 打印致命错误并等待回车再退出：
// 双击启动时窗口不会一闪而过，用户能看到原因；
// stdin 被重定向/管道时读到 EOF 立即返回，不会挂住自动化调用。
func fatalWait(format string, v ...any) {
	fmt.Println()
	fmt.Printf(format+"\n", v...)
	fmt.Println()
	fmt.Print("按回车键退出……")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
