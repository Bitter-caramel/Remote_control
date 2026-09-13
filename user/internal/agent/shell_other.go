//go:build !windows

package agent

// startShell 非 Windows 平台（开发/自测用）：常驻 sh 管道。
func startShell() (Shell, error) {
	return newPipeCmd("sh", "-i")
}
