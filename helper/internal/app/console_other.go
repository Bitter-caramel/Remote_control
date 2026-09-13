//go:build !windows

// 非 Windows 平台（开发/自测用）：终端默认 UTF-8 且通常自带 raw 模式支持，
// 这里只提供空实现，面板走按行读取的降级路径。

package app

func enableUTF8Console() {}

func enterRawTerminal() (restore func(), ok bool, cols, rows int) {
	return func() {}, false, 120, 30
}
