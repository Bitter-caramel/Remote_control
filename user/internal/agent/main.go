package agent

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"remoteassist-user/protocol"
)

// activeClient 记录当前活动的连接，用于收到退出信号时 best-effort 发送下线通知。
var (
	activeClientMu sync.Mutex
	activeClient   *Client
)

func setActiveClient(c *Client) {
	activeClientMu.Lock()
	activeClient = c
	activeClientMu.Unlock()
}

// Run 用户端主入口（由 cmd/user 调用）
func Run() {
	// 把控制台代码页切到 UTF-8，保证中文提示正常显示
	enableUTF8Console()

	// 1. 检测 config 文件：不存在则创建（首次上线），存在则读取 userID
	cfg, err := LoadConfig()
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
	fmt.Printf("本机系统: %s\n", DetectOS())
	fmt.Printf("正在连接协助者 %s ……\n", cfg.ServerAddr)

	// 2. 退出信号处理：在重连循环之外只注册一次，覆盖 Ctrl+C(SIGINT)、
	//    Ctrl+\(SIGQUIT)、SIGTERM。避免每次重连重复注册/泄漏导致信号无法退出。
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, exitSignals()...)
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
	if ign := ignoreSignals(); len(ign) > 0 {
		stp := make(chan os.Signal, 1)
		signal.Notify(stp, ign...)
		go func() {
			for range stp {
			}
		}()
	}

	// 3. 主循环：保持长连接；断开后自动重连
	for {
		if err := session(cfg); err != nil {
			fmt.Printf("与协助者 (%s) 连接中断: %v，%d 秒后重试\n", cfg.ServerAddr, err, int64(retryInterval/time.Second))
		}
		time.Sleep(retryInterval)
	}
}

// retryInterval 断线重连间隔
const retryInterval = 5 * time.Second

// session 一次完整的连接会话：连接 → 注册 → 启动 shell → 心跳 → 转发终端，阻塞至断开
func session(cfg *Config) error {
	conn, err := Dial(cfg.ServerAddr)
	if err != nil {
		return err
	}
	c := &Client{cfg: cfg, Conn: conn}
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

	// 常驻交互式 shell（首次有输入时启动，退出自动重启）
	c.shell = &ShellManager{c: c}
	defer c.shell.Close()
	go c.shell.Pump() // shell 输出 → 协助者终端

	// 周期发送心跳
	stop := make(chan struct{})
	defer close(stop)
	StartHeartbeat(c, stop)

	fmt.Println("已连接协助者服务器，等待指令。")
	c.ReadLoop() // 阻塞转发协助者输入，直到连接断开
	return fmt.Errorf("连接被关闭")
}
