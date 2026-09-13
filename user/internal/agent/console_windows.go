//go:build windows

package agent

import "syscall"

var (
	k32        = syscall.NewLazyDLL("kernel32.dll")
	procGetACP = k32.NewProc("GetACP")
)

// enableUTF8Console 把本进程控制台的输入/输出代码页切到 65001(UTF-8)，
// 这样程序打印的 UTF-8 中文在传统 cmd 窗口里也不会乱码。
func enableUTF8Console() {
	k32.NewProc("SetConsoleCP").Call(65001)
	k32.NewProc("SetConsoleOutputCP").Call(65001)
}

// ansicp936 判断系统 ANSI 代码页是否为 936（GBK，简体中文）。
// 一次性命令的输出编码跟随系统代码页，只有 936 才需要按 GBK 解码。
func ansicp936() bool {
	cp, _, _ := procGetACP.Call()
	return cp == 936
}
