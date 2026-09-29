//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached 在 Unix 上以守护方式重新拉起自身：
// Setsid 脱离终端会话 + stdio 全部重定向，父进程随即退出，子进程独立运行。
func spawnDetached() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devnull.Close()

	logf, err := openLogFile()
	if err != nil {
		return 0, err
	}
	defer logf.Close()

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), daemonEnv+"=1")
	cmd.Stdin = devnull
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid    // Release 之后 Pid 会被置为 -1，必须先取
	_ = cmd.Process.Release() // 父进程放手，子进程不随父退出
	return pid, nil
}

// processAlive Unix 下用 kill(pid, 0) 探测：返回 nil 或 EPERM 都说明进程存在。
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// signalStop 发送 SIGTERM，触发程序内的优雅退出（发下线通知给协助者后退出）。
func signalStop(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
