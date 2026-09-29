//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// detachedProcess 等价于 winbase.h 的 DETACHED_PROCESS(0x8)，
// syscall 包未导出该常量，此处自行定义。
const detachedProcess = 0x00000008

// spawnDetached 在 Windows 上以脱离控制台方式重新拉起自身，不占用命令窗口。
func spawnDetached() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := openLogFile()
	if err != nil {
		return 0, err
	}
	defer logf.Close()

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), daemonEnv+"=1")
	cmd.Stdin = logf
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// DETACHED_PROCESS: 不创建控制台窗口；CREATE_NEW_PROCESS_GROUP: 独立进程组
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid    // Release 之后 Pid 会被置为 -1，必须先取
	_ = cmd.Process.Release() // 父进程放手，子进程不随父退出
	return pid, nil
}

// processAlive Windows 下用 OpenProcess + 退出码探测进程是否存活。
func processAlive(pid int) bool {
	const processQueryLimitedInfo = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// signalStop Windows 无 SIGTERM，直接终止进程（优雅下线通知不适用）。
func signalStop(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
