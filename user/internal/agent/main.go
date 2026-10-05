package agent

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"remoteassist-user/internal/core"
	"remoteassist-user/internal/sys"
	"remoteassist-user/internal/term"
	"remoteassist-user/protocol"
)

// activeClient 记录当前活动的连接，用于收到退出信号时 best-effort 发送下线通知。
var (
	activeClientMu sync.Mutex
	activeClient   *core.Client
)

func setActiveClient(c *core.Client) {
	activeClientMu.Lock()
	activeClient = c
	activeClientMu.Unlock()
}

// Run 用户端主入口（由 cmd/user 调用）
func Run() {
	// 把控制台代码页切到 UTF-8，保证中文提示正常显示
	sys.EnableUTF8Console()

	// 1. 检测 config 文件：不存在则创建（首次上线），存在则读取 userID
	cfg, err := core.LoadConfig()
	if err != nil {
		fmt.Println("读取配置失败:", err)
		return
	}
	if cfg.FirstTime() {
		fmt.Println("首次运行，已创建 config.json，正在向协助者申请 userID……")
	} else {
		who := cfg.UserID
		if cfg.Name != "" {
			who += "（" + cfg.Name + "）"
		}
		fmt.Printf("已读取本地身份: %s（服务器: %s）\n", who, cfg.ServerAddr)
	}
	fmt.Printf("本机系统: %s\n", sys.DetectOS())
	fmt.Println("提示: 后台运行用 `user start`，停止用 `user stop`")
	fmt.Printf("正在连接协助者 %s ……\n", cfg.ServerAddr)

	// 2. 退出信号处理：在重连循环之外只注册一次，覆盖 Ctrl+C(SIGINT)、
	//    Ctrl+\(SIGQUIT)、SIGTERM。避免每次重连重复注册/泄漏导致信号无法退出。
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, sys.ExitSignals()...)
	go func() {
		<-sigs
		fmt.Println("\n正在退出……")
		activeClientMu.Lock()
		c := activeClient
		activeClientMu.Unlock()
		if c != nil {
			// best-effort 发送下线通知，不阻塞退出
			go func() { _ = c.Send(&protocol.Message{Type: protocol.TypeBye}) }()
			time.Sleep(300 * time.Millisecond)
		}
		os.Exit(0)
	}()

	// 忽略 Ctrl+Z(SIGTSTP)：被挂起会表现为"卡死不动"
	if ign := sys.IgnoreSignals(); len(ign) > 0 {
		stp := make(chan os.Signal, 1)
		signal.Notify(stp, ign...)
		go func() {
			for range stp {
			}
		}()
	}

	// 3. 主循环：保持长连接；断开后自动重连。
	//    操作上下文管理器是进程级的：重连只切换输出通道，不销毁已有 shell，
	//    否则一旦网络抖动，用户正在跑的进程和 cwd 就全丢了。
	sessions := term.NewSessionManager()
	for {
		if err := session(cfg, sessions); err != nil {
			fmt.Printf("与协助者 (%s) 连接中断: %v，%d 秒后重试\n", cfg.ServerAddr, err, int64(retryInterval/time.Second))
		}
		time.Sleep(retryInterval)
	}
}

// retryInterval 断线重连间隔
const retryInterval = 5 * time.Second

// session 一次完整的连接会话：连接 → 注册 → 挂载上下文管理器 → 心跳 → 转发终端，阻塞至断开
func session(cfg *core.Config, sessions *term.SessionManager) error {
	conn, err := core.Dial(cfg.ServerAddr)
	if err != nil {
		return err
	}
	c := core.NewClient(cfg, conn)
	c.Sessions = sessions
	setActiveClient(c)
	defer setActiveClient(nil)
	defer conn.Close()

	// 注册身份：服务端返回确认/分配的 userID
	uid, err := c.Register()
	if err != nil {
		return fmt.Errorf("注册失败: %w", err)
	}
	if uid != cfg.UserID {
		// 首次上线：把服务端分配的 botID/userID 保存到本地 config
		cfg.UserID = uid
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("保存 userID 失败: %w", err)
		}
		fmt.Println("已获取 userID:", uid)
	}

	// 注册成功后才把输出通道指向本连接；之后重连会换成新连接。
	// 断开期间 Sender 为 nil，各上下文的输出直接丢弃（不会阻塞也不会串到新连接）。
	sessions.SetSender(c)
	defer sessions.SetSender(nil)

	// 周期发送心跳
	stop := make(chan struct{})
	defer close(stop)
	core.StartHeartbeat(c, stop)

	fmt.Println("已连接协助者服务器，等待指令。")
	c.ReadLoop() // 阻塞分发协助者消息，直到连接断开
	return fmt.Errorf("连接被关闭")
}
