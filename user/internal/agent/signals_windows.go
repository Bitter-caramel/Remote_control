//go:build windows

package agent

import "os"

// exitSignals 触发"优雅退出"的信号（Windows：Ctrl+C / Ctrl+Break）
func exitSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// ignoreSignals 需要忽略（避免进程被挂起/误退出）的信号；Windows 无对应项
func ignoreSignals() []os.Signal { return nil }
