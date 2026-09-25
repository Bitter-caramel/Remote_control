package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 后台运行子命令：user start / user stop / user status。
// pid 文件与 config.json 一样放在当前工作目录，因此 start/stop/status 需要在同一目录下执行。

const (
	daemonEnv   = "RA_DAEMON" // 环境变量标记：当前进程是被 start 重新拉起的守护子进程
	pidFilePath = "user.pid"  // pid 文件，stop/status 据此找到守护进程
	logFileDir  = "logs"      // 守护进程日志目录（相对当前工作目录）
	logFileName = "user.log"  // 守护进程 stdout/stderr 输出
)

// IsDaemonChild 当前进程是否守护子进程（由 `user start` 用 RA_DAEMON=1 重新拉起）。
func IsDaemonChild() bool { return os.Getenv(daemonEnv) == "1" }

// DaemonMain 守护子进程入口：先写 pid 文件，再进入主循环。
func DaemonMain() {
	if err := writePIDFile(); err != nil {
		fmt.Println("写入 pid 文件失败:", err)
		return
	}
	Run()
}

// HandleDaemonCommand 处理 start / stop / status 子命令。
// 返回 true 表示参数已被处理（含未知参数提示）；返回 false 表示无参数，应由调用方进入前台运行。
func HandleDaemonCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "start":
		cmdStart()
	case "stop":
		cmdStop()
	case "status":
		cmdStatus()
	default:
		fmt.Printf("未知参数: %s。可用: start / stop / status；不带参数为前台运行。\n", args[0])
	}
	return true
}

// cmdStart 后台启动：重新拉起自身并脱离终端会话。
func cmdStart() {
	if pid, ok := runningPID(); ok {
		fmt.Printf("已在运行 (pid %d)，无需重复启动。\n", pid)
		return
	}
	pid, err := spawnDetached()
	if err != nil {
		fmt.Println("后台启动失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已在后台启动 (pid %d)，日志: %s\n", pid, filepath.Join(logFileDir, logFileName))
}

// cmdStop 按 pid 文件停止守护进程。
func cmdStop() {
	pid, ok := loadPID()
	if !ok {
		fmt.Println("未在运行（找不到 pid 文件）。")
		return
	}
	if !processAlive(pid) {
		fmt.Printf("进程 %d 已不存在，清理残留 pid 文件。\n", pid)
		_ = os.Remove(pidFilePath)
		return
	}
	if err := signalStop(pid); err != nil {
		fmt.Println("停止失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已发送停止信号 (pid %d)。\n", pid)
}

// cmdStatus 查询守护进程状态。
func cmdStatus() {
	pid, ok := loadPID()
	if !ok {
		fmt.Println("未在运行（无 pid 文件）。")
		return
	}
	if processAlive(pid) {
		fmt.Printf("运行中 (pid %d)\n", pid)
	} else {
		fmt.Printf("未在运行（pid 文件残留，进程 %d 已退出）。\n", pid)
	}
}

// runningPID 返回正在运行的守护进程 pid（有 pid 文件且进程存活）。
func runningPID() (int, bool) {
	pid, ok := loadPID()
	if !ok || !processAlive(pid) {
		return 0, false
	}
	return pid, true
}

func writePIDFile() error {
	return os.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func loadPID() (int, bool) {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// openLogFile 打开（必要时创建）守护进程日志文件 logs/user.log。
func openLogFile() (*os.File, error) {
	if err := os.MkdirAll(logFileDir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(logFileDir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
