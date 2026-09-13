//go:build !windows && !linux

package agent

// startShell 其它类 Unix 平台（macOS 等开发/自测用）：常驻 sh 管道。
// 管道没有终端行规程：无回显、无法处理 Ctrl+C 等控制字符，
// 仅保证基本命令可用；Linux 使用 pty_unix.go 的真伪终端实现。
func startShell() (Shell, error) {
	return newPipeCmd("sh", "-i")
}
