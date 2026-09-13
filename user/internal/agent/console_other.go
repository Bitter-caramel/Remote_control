//go:build !windows

package agent

// enableUTF8Console 非 Windows 终端默认 UTF-8，无需处理
func enableUTF8Console() {}

// ansicp936 非 Windows 平台不存在 GBK 代码页问题
func ansicp936() bool { return false }
