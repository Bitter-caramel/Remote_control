package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"remoteassist-user/protocol"
)

// Shell 一个常驻的交互式 shell（PTY 或降级管道）。
// 与旧的"每条命令新开 cmd /c"不同，shell 在整个连接期间存活：
// 当前目录、环境变量、交互式程序状态全部保持。
type Shell interface {
	Read(p []byte) (int, error)  // 读终端输出（原始字节）
	Write(p []byte) (int, error) // 写键盘输入（原始字节）
	Resize(cols, rows int)       // 调整终端尺寸（不支持时为空操作）
	Close() error
}

// startShell 在当前平台启动一个交互式 shell，初始工作目录为用户主目录。
// Windows 实现见 shell_windows.go（ConPTY 优先，管道降级），
// 其他平台见 shell_other.go。

// ShellManager 管理当前连接的 shell：惰性启动、退出自动重启、输出泵送。
type ShellManager struct {
	c *Client

	mu     sync.Mutex
	shell  Shell
	closed bool
	cols   int // 最近一次窗口尺寸（shell 惰性创建时也要使用）
	rows   int
}

// Write 把协助者的键盘输入写入 shell，首次写入时启动 shell
func (s *ShellManager) Write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell 已关闭")
	}
	if s.shell == nil {
		sh, err := startShell()
		if err != nil {
			return err
		}
		if s.cols > 0 && s.rows > 0 {
			sh.Resize(s.cols, s.rows)
		}
		s.shell = sh
	}
	_, err := s.shell.Write(p)
	return err
}

// Resize 转发尺寸变化（shell 尚未启动时只记录）
func (s *ShellManager) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
	if s.shell != nil {
		s.shell.Resize(cols, rows)
	}
}

// Close 结束 shell（连接断开时调用）
func (s *ShellManager) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.shell != nil {
		s.shell.Close()
		s.shell = nil
	}
}

// restart 旧 shell 退出后重启一个新的
func (s *ShellManager) restart(old Shell) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.shell == old {
		s.shell = nil
	}
	s.mu.Unlock()
	old.Close()
}

// Pump 持续读取 shell 输出并发送给协助者；shell 退出（如输入了 exit）后自动重启。
func (s *ShellManager) Pump() {
	buf := make([]byte, 8192)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		sh := s.shell
		s.mu.Unlock()
		if sh == nil {
			// shell 惰性启动：还没收到任何输入时不创建
			time.Sleep(200 * time.Millisecond)
			continue
		}

		n, err := sh.Read(buf)
		if n > 0 {
			// 拷贝后再异步发送，避免复用 buf 产生数据竞争
			data := make([]byte, n)
			copy(data, buf[:n])
			if sendErr := s.c.Send(&protocol.Message{
				Type: protocol.TypeOutput,
				Data: protocol.EncodeB64(data),
			}); sendErr != nil {
				return // 连接已断
			}
		}
		if err != nil {
			if s.closed {
				return
			}
			s.restart(sh) // shell 退出，下轮惰性重建
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// homeDir 返回 shell 的初始工作目录（用户主目录，而不是 user.exe 所在目录）
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// pipeShell 降级方案：常驻 shell 进程 + 管道（非 PTY）。
// 当前目录/环境变量可以保持，但直接读控制台输入的交互式程序无法使用。
type pipeShell struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (s *pipeShell) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *pipeShell) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *pipeShell) Resize(cols, rows int)       {} // 管道没有终端尺寸概念

func (s *pipeShell) Close() error {
	s.in.Close()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.out.Close()
	return s.cmd.Wait()
}

// newPipeCmd 构造一个输出合并到管道的常驻命令
func newPipeCmd(name string, args ...string) (*pipeShell, error) {
	c := exec.Command(name, args...)
	c.Dir = homeDir()

	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Stdout = w
	c.Stderr = w
	if err := c.Start(); err != nil {
		return nil, err
	}
	w.Close() // 父进程只保留读端
	return &pipeShell{cmd: c, in: stdin, out: r}, nil
}
