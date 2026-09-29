package agent

// 后台管理（user start / stop / status）在本包只保留入口，
// 具体实现（pid 文件、拉守护子进程、平台差异）见 internal/daemon。

import (
	"fmt"

	"remoteassist-user/internal/daemon"
)

// IsDaemonChild 当前进程是否守护子进程（由 `user start` 用 RA_DAEMON=1 重新拉起）。
func IsDaemonChild() bool { return daemon.IsChild() }

// DaemonMain 守护子进程入口：先写 pid 文件，再进入主循环。
func DaemonMain() {
	if err := daemon.WritePIDFile(); err != nil {
		fmt.Println("写入 pid 文件失败:", err)
		return
	}
	Run()
}

// HandleDaemonCommand 处理 start / stop / status 子命令。
// 返回 true 表示参数已被处理（含未知参数提示）；返回 false 表示无参数，应由调用方进入前台运行。
func HandleDaemonCommand(args []string) bool { return daemon.HandleCommand(args) }
