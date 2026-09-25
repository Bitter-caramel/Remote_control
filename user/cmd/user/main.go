// 用户端（被控机）入口：所有逻辑在 internal/agent，这里只负责启动与 start/stop/status 分发。
package main

import (
	"os"

	"remoteassist-user/internal/agent"
)

func main() {
	// 守护子进程（由 `user start` 重新拉起，带 RA_DAEMON=1）直接进入主循环
	if agent.IsDaemonChild() {
		agent.DaemonMain()
		return
	}
	// start / stop / status 后台管理子命令；无参数则前台运行
	if agent.HandleDaemonCommand(os.Args[1:]) {
		return
	}
	agent.Run()
}
