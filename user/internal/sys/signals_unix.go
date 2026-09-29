//go:build !windows

package sys

import (
	"os"
	"syscall"
)

// ExitSignals 触发"优雅退出"的信号：Ctrl+C(SIGINT)、Ctrl+\(SIGQUIT)、SIGTERM
func ExitSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT}
}

// IgnoreSignals Ctrl+Z(SIGTSTP)：本程序是被控端常驻进程，
// 默认行为会把进程挂起，表现为"卡死不动"，必须忽略
func IgnoreSignals() []os.Signal {
	return []os.Signal{syscall.SIGTSTP}
}
