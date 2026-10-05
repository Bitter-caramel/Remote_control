package term

import (
	"io"
	"os"
	"os/exec"

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

// startShell 在当前平台启动一个交互式 shell，初始工作目录由调用方指定
// （一般经 workDir 取「服务端记录的 cwd」或用户主目录）。
// Windows 实现见 shell_windows.go（ConPTY 优先，管道降级），
// 其他平台见 shell_other.go。

// Sender 是 SessionManager 回传终端输出所需的最小能力（由 core.Client 实现）。
// 用接口而非直接引用 core.Client：user 端 core 需要持有 SessionManager 并在读循环中
// 调用它，若这里反向依赖 core 会形成包循环导入，故在此收敛为最小接口。
type Sender interface {
	Send(msg *protocol.Message) error
}

// HomeDir 返回 shell 的初始工作目录（用户主目录，而不是 user.exe 所在目录）
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// workDir 选定新建 shell 的初始工作目录：优先用服务端带下来的 cwd（bot 重启后恢复现场），
// 为空或该路径已不存在 / 不是目录时回落到用户主目录。
func workDir(cwd string) string {
	if cwd != "" {
		if info, err := os.Stat(cwd); err == nil && info.IsDir() {
			return cwd
		}
	}
	return HomeDir()
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

// newPipeCmd 构造一个输出合并到管道的常驻命令；env 为 nil 时继承当前进程环境，
// dir 为初始工作目录
func newPipeCmd(env []string, dir string, name string, args ...string) (*pipeShell, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = env

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
