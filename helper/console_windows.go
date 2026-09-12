//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	k32                      = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCP         = k32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP   = k32.NewProc("SetConsoleOutputCP")
	procGetStdHandle         = k32.NewProc("GetStdHandle")
	procGetConsoleMode       = k32.NewProc("GetConsoleMode")
	procSetConsoleMode       = k32.NewProc("SetConsoleMode")
	procGetConsoleScreenInfo = k32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	stdInputHandle  = uint32(0xFFFFFFF6) // -10
	stdOutputHandle = uint32(0xFFFFFFF5) // -11

	enableVirtualTerminalInput      = 0x0200
	enableProcessedOutput           = 0x0001
	enableWrapAtEolOutput           = 0x0002
	enableVirtualTerminalProcessing = 0x0004
)

type (
	coord struct {
		X, Y int16
	}
	smallRect struct {
		Left, Top, Right, Bottom int16
	}
	consoleScreenBufferInfo struct {
		Size              coord
		CursorPosition    coord
		Attributes        uint16
		Window            smallRect
		MaximumWindowSize coord
	}
)

// enableUTF8Console 把控制台代码页切到 65001(UTF-8)，中文不再乱码
func enableUTF8Console() {
	procSetConsoleCP.Call(65001)
	procSetConsoleOutputCP.Call(65001)
}

func stdHandle(which uint32) syscall.Handle {
	h, _, _ := procGetStdHandle.Call(uintptr(which))
	return syscall.Handle(h)
}

// enterRawTerminal 把控制台切到终端透传模式：
//   - 输入：ENABLE_VIRTUAL_TERMINAL_INPUT，按键以 VT 字节流送达（Ctrl+C 变成 0x03，
//     不再杀掉本程序，而是透传给远端 shell）；
//   - 输出：开启 VT 处理，远端 shell 的颜色/光标控制序列正常渲染。
//
// 返回 restore（必须调用以还原控制台）、是否成功（stdin 不是控制台时为 false，
// 例如输入被管道重定向，调用方应走按行读取的降级路径）、当前窗口列/行数。
func enterRawTerminal() (restore func(), ok bool, cols, rows int) {
	cols, rows = 120, 30

	hIn := stdHandle(stdInputHandle)
	var inMode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(hIn), uintptr(unsafe.Pointer(&inMode))); r == 0 {
		return func() {}, false, cols, rows // stdin 不是控制台
	}
	hOut := stdHandle(stdOutputHandle)
	var outMode uint32
	procGetConsoleMode.Call(uintptr(hOut), uintptr(unsafe.Pointer(&outMode)))

	// 记录窗口尺寸
	var csbi consoleScreenBufferInfo
	if procGetConsoleScreenInfo.Call(uintptr(hOut), uintptr(unsafe.Pointer(&csbi))); csbi.Window.Right > csbi.Window.Left {
		cols = int(csbi.Window.Right - csbi.Window.Left + 1)
		rows = int(csbi.Window.Bottom - csbi.Window.Top + 1)
	}

	procSetConsoleMode.Call(uintptr(hIn), uintptr(enableVirtualTerminalInput))
	procSetConsoleMode.Call(uintptr(hOut),
		uintptr(enableProcessedOutput|enableWrapAtEolOutput|enableVirtualTerminalProcessing))

	return func() {
		procSetConsoleMode.Call(uintptr(hIn), uintptr(inMode))
		procSetConsoleMode.Call(uintptr(hOut), uintptr(outMode))
	}, true, cols, rows
}
