package app

// app：协助者服务端的启动装配（bootstrap）。
// 只负责按顺序拉起各模块并把它们接到一起，
// 具体能力分散在各子包：
//
//	core    在线机器状态（BOT / Manager / 心跳 / exec 等待）
//	uplink  被控端（user）连入侧监听
//	ipc     本机接入：终端窗口(/attach) 与 CLI(/cli)
//	panel   前台 DOS 命令面板
//	term    控制台原始模式（跨平台）
//	webui   Web 控制台（浏览器终端）
//	store   本地数据库（账号/权限/机器等级/会话/审计）
//	auth    登录鉴权（口令慢哈希、会话令牌、角色门禁）

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/ipc"
	"remoteassist-helper/internal/panel"
	"remoteassist-helper/internal/store"
	"remoteassist-helper/internal/term"
	"remoteassist-helper/internal/uplink"
	"remoteassist-helper/internal/webui"
	"remoteassist-helper/logx"
)

// 服务端固定配置
const (
	ListenAddr = ":8080"      // 对外监听端口（Web 控制台与用户端接入共用）
	LogDir     = "logs"       // 日志目录（program.log / bots.log / botlogs/<botID>.log）
	DBFile     = "helper.db"  // 数据库文件名（放在日志目录下）
	KeyFile    = "helper.key" // 令牌签名密钥文件名（权限 0600）
)

// Run 协助者服务端主入口（由 cmd/helper 调用）
func Run() {
	// 终端窗口模式（由主面板 `bot [ID]` 弹新窗口拉起），提前拦截 flag，
	// 不初始化任何监听和日志
	if len(os.Args) >= 2 && os.Args[1] == "-attach" {
		os.Exit(ipc.RunAttachMode())
	}
	// CLI/Agent 模式：脚本化查询/执行，连接到正在运行的主程序
	if len(os.Args) >= 2 && os.Args[1] == "-cli" {
		os.Exit(ipc.RunCLIMode())
	}
	flag.Parse()

	// 控制台切到 UTF-8，保证 bot 回显中的中文在传统 cmd 窗口也能正常显示
	term.EnableUTF8Console()

	// 初始化三类日志
	prog, err := logx.NewProgramLog(LogDir)
	if err != nil {
		fmt.Println("初始化 programlog 失败:", err)
		return
	}
	defer prog.Close()
	botslog := logx.NewBotsLog(LogDir)

	// 本地数据库：账号与权限、机器等级、登录会话、审计日志
	st, err := store.Open(filepath.Join(LogDir, DBFile))
	if err != nil {
		prog.Error("打开数据库失败: %v", err)
		fatalWait("打开数据库失败：%v\n（数据库位于 %s，若文件损坏可将其删除后重启，账号需重建）",
			err, filepath.Join(LogDir, DBFile))
		return
	}
	defer st.Close()

	// 令牌签名密钥：首次启动自动生成，之后必须保持不变，
	// 否则所有已登录用户的令牌会立刻失效
	key, err := auth.LoadOrCreateKey(filepath.Join(LogDir, KeyFile))
	if err != nil {
		prog.Error("加载签名密钥失败: %v", err)
		fatalWait("加载签名密钥失败：%v", err)
		return
	}
	authenticator := auth.New(st, key, auth.SessionTTL)

	// 全新部署时创建初始管理员，随机口令只在本次启动打印一次
	if err := bootstrapAdmin(st, prog); err != nil {
		prog.Error("创建初始管理员失败: %v", err)
		fatalWait("创建初始管理员失败：%v", err)
		return
	}
	startSessionJanitor(st, prog)

	// bots 管理器（bots 列表 + 缓存区列表）
	m := core.NewManager(LogDir, prog, botslog)

	// 本地 IPC：终端窗口与 CLI/Agent 通过它桥接到对应 bot
	ipcSrv, err := ipc.NewServer(m, LogDir)
	if err != nil {
		prog.Error("启动本地终端接入服务失败: %v", err)
		fmt.Println("启动本地终端接入服务失败:", err)
		return
	}
	defer ipcSrv.Close()

	// 后台：心跳检测
	core.StartHeartbeatChecker(m, prog)

	// 后台：闲置下线确认（占用者长时间无输入且机器无输出 → 询问是否下线）
	core.StartIdleWatch(m, prog)

	// 后台：监听端口，接收用户端连接（TCP → WebSocket）
	// 可用环境变量 RA_LISTEN 覆盖监听地址，如 :18080
	listenAddr := ListenAddr
	if v := os.Getenv("RA_LISTEN"); v != "" {
		listenAddr = v
	}
	ln, err := uplink.NewListener(listenAddr)
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

	// 一个端口两套服务：/ws 给被控端接入，其余路径是 Web 控制台（需登录）
	mux := http.NewServeMux()
	mux.Handle("/ws", uplink.Handler(m, prog, st))
	webui.Register(mux, m, prog, st, authenticator)
	go uplink.Serve(ln, mux, prog)

	fmt.Printf("[%s] Web 控制台已就绪: http://<本机IP>%s/  （同网段设备用浏览器访问，需登录）\n",
		time.Now().Format("15:04:05"), ln.Addr().String())

	// 前台：DOS 控制面板（阻塞，直到输入 exit）
	panel.RunPanel(os.Stdin, os.Stdout, m, prog, botslog, ipcSrv)
}

// bootstrapAdmin 全新部署（账号表为空）时创建初始管理员。
// 随机口令只在本次启动打印一次，服务端只保存加盐哈希。
func bootstrapAdmin(st *store.Store, prog *logx.ProgramLog) error {
	n, err := st.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	password, err := auth.RandomPassword()
	if err != nil {
		return err
	}
	hash, salt, iterations, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser("admin", "管理员", hash, salt, iterations, store.RoleAdmin); err != nil {
		return err
	}

	const line = "============================================================"
	fmt.Println(line)
	fmt.Println(" 首次启动：已创建初始管理员账号")
	fmt.Println("   账号: admin")
	fmt.Println("   口令: " + password)
	fmt.Println(" 该口令只显示这一次，请登录后立即在【用户管理】中修改。")
	fmt.Println(line)
	prog.Info("已创建初始管理员账号 admin（口令仅本次启动在控制台显示）")
	return nil
}

// startSessionJanitor 定期清理过期/已撤销的会话记录，避免表无限增长。
// 只删历史记录，不影响正在进行中的登录。
func startSessionJanitor(st *store.Store, prog *logx.ProgramLog) {
	const (
		interval = 6 * time.Hour
		keep     = 7 * 24 * time.Hour
	)
	go func() {
		for {
			time.Sleep(interval)
			if n, err := st.PruneSessions(time.Now(), keep); err != nil {
				prog.Error("清理过期会话失败: %v", err)
			} else if n > 0 {
				prog.Info("已清理 %d 条过期会话记录", n)
			}
		}
	}()
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
