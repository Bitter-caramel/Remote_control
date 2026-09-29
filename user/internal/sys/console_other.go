//go:build !windows

package sys

// EnableUTF8Console 非 Windows 终端默认 UTF-8，无需处理
func EnableUTF8Console() {}

// ANSICP936 非 Windows 平台不存在 GBK 代码页问题
func ANSICP936() bool { return false }
