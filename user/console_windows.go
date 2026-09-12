//go:build windows

package main

import "syscall"

// enableUTF8Console 把本进程控制台的输入/输出代码页切到 65001(UTF-8)，
// 这样程序打印的 UTF-8 中文在传统 cmd 窗口里也不会乱码。
func enableUTF8Console() {
	k := syscall.NewLazyDLL("kernel32.dll")
	k.NewProc("SetConsoleCP").Call(65001)
	k.NewProc("SetConsoleOutputCP").Call(65001)
}
