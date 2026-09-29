//go:build windows

package sys

import "os"

// ExitSignals 触发"优雅退出"的信号（Windows：Ctrl+C / Ctrl+Break）
func ExitSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// IgnoreSignals 需要忽略（避免进程被挂起/误退出）的信号；Windows 无对应项
func IgnoreSignals() []os.Signal { return nil }
