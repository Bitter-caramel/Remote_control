//go:build linux

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// startShell Linux：基于 /dev/ptmx 启动真正的伪终端（PTY）。
// 回显、行编辑、信号转换（Ctrl+C→SIGINT、Ctrl+Z→SIGTSTP、Ctrl+D→EOF）
// 全部由内核行规程完成，协助端的远程终端体验与本机终端一致。
// 这与 Windows 端的 ConPTY 是对等实现；旧的管道 shell 无法回显、
// 也无法处理控制字符，会造成"屏幕只有 $ 提示符、按键无反应"。
func startShell() (Shell, error) {
	// 1) 打开 PTY 主端
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 /dev/ptmx 失败: %w", err)
	}
	cleanup := func(e error) (Shell, error) {
		master.Close()
		return nil, e
	}

	// 2) 取得从端编号（TIOCGPTN）并解锁（TIOCSPTLCK=0）
	var ptn uint32
	if err := ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&ptn)); err != nil {
		return cleanup(fmt.Errorf("获取 pty 编号失败: %w", err))
	}
	unlock := int32(0)
	if err := ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return cleanup(fmt.Errorf("解锁 pty 失败: %w", err))
	}

	// 3) 打开从端（O_NOCTTY：控制终端由子进程通过 Setctty 获得）
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return cleanup(fmt.Errorf("打开 pty 从端失败: %w", err))
	}

	// 4) 选择 shell 与环境
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	env := os.Environ()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color")
	}

	// 5) 以新会话启动 shell，并把从端设为其控制终端：
	//    Setsid+Setctty 之后 shell 才是会话首进程，作业控制（Ctrl+C/Ctrl+Z）才生效。
	cmd := exec.Command(shell, "-i")
	cmd.Dir = homeDir()
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return cleanup(fmt.Errorf("启动 %s 失败: %w", shell, err))
	}
	slave.Close() // 父进程只保留主端

	p := &ptyShell{master: master, cmd: cmd}
	p.Resize(120, 30) // 先给一个初始尺寸，随后被真实窗口尺寸覆盖
	return p, nil
}

// ptyShell 基于伪终端的常驻 shell
type ptyShell struct {
	master *os.File
	cmd    *exec.Cmd
}

func (s *ptyShell) Read(p []byte) (int, error)  { return s.master.Read(p) }
func (s *ptyShell) Write(p []byte) (int, error) { return s.master.Write(p) }

// Resize 通过 TIOCSWINSZ 调整窗口尺寸；内核会自动向前台进程组发送 SIGWINCH
func (s *ptyShell) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	ws := struct{ rows, cols, x, y uint16 }{uint16(rows), uint16(cols), 0, 0}
	_ = ioctl(s.master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

func (s *ptyShell) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.master.Close()
	return s.cmd.Wait()
}

// ioctl 裸 ioctl 系统调用封装（纯 syscall，无需 cgo，兼容 CGO_ENABLED=0 交叉编译）
func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}
