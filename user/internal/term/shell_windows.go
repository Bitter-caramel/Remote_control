//go:build windows

package term

import (
	"io"
	"os"

	"github.com/UserExistsError/conpty"
)

// startShell 优先使用 ConPTY（Windows 10 1809+）启动真正的伪终端：
// 支持交互式程序（diskpart、netsh、python 等）、颜色/光标控制、当前目录保持。
// 系统过老不支持 ConPTY 时，降级为常驻 cmd.exe 管道 shell（目录仍可保持，
// 但直接读控制台的交互式程序不可用）。
//
// 启动时执行 chcp 65001，让终端输出统一为 UTF-8，避免中文乱码；
// 提示符哨兵通过 PROMPT 环境变量注入（不可见），不能写进 shell（会被回显）。
// dir 为初始工作目录（bot 重启后由服务端从库里带下来，用于恢复现场）。
func startShell(nonce string, dir string) (Shell, error) {
	name, value := promptEnv(nonce)
	env := append(os.Environ(), name+"="+value)

	if conpty.IsConPtyAvailable() {
		c, err := conpty.Start(
			`cmd.exe /K chcp 65001>nul`,
			conpty.ConPtyWorkDir(dir),
			conpty.ConPtyDimensions(120, 30),
			conpty.ConPtyEnv(env),
		)
		if err == nil {
			return &conptyShell{p: c}, nil
		}
	}
	return startPipeShell(env, dir)
}

// conptyShell 把第三方 ConPty 适配为 Shell 接口
type conptyShell struct{ p *conpty.ConPty }

func (s *conptyShell) Read(p []byte) (int, error)  { return s.p.Read(p) }
func (s *conptyShell) Write(p []byte) (int, error) { return s.p.Write(p) }
func (s *conptyShell) Resize(cols, rows int)       { _ = s.p.Resize(cols, rows) }
func (s *conptyShell) Close() error                { return s.p.Close() }

// startPipeShell ConPTY 不可用时的降级方案：一个常驻 cmd.exe 进程 + 管道
func startPipeShell(env []string, dir string) (Shell, error) {
	s, err := newPipeCmd(env, dir, "cmd.exe")
	if err != nil {
		return nil, err
	}
	// 统一 UTF-8 代码页（管道无行规程，不会回显这行）
	io.WriteString(s.in, "chcp 65001>nul\r\n")
	return s, nil
}
