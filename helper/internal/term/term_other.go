//go:build !windows

// 非 Windows 平台（开发/自测用）：终端默认 UTF-8 且通常自带 raw 模式支持，
// 这里只提供空实现，面板走按行读取的降级路径。

package term

// EnableUTF8Console 非 Windows 平台控制台默认已是 UTF-8
func EnableUTF8Console() {}

// EnterRawTerminal 非 Windows 平台不提供控制台透传，调用方走按行读取的降级路径
func EnterRawTerminal() (restore func(), ok bool, cols, rows int) {
	return func() {}, false, 120, 30
}
