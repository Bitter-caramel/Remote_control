//go:build windows

package main

import (
	"io"

	"github.com/UserExistsError/conpty"
)

// startShell 优先使用 ConPTY（Windows 10 1809+）启动真正的伪终端：
// 支持交互式程序（diskpart、netsh、python 等）、颜色/光标控制、当前目录保持。
// 系统过老不支持 ConPTY 时，降级为常驻 cmd.exe 管道 shell（目录仍可保持，
// 但直接读控制台的交互式程序不可用）。
//
// 启动时执行 chcp 65001，让终端输出统一为 UTF-8，避免中文乱码。
func startShell() (Shell, error) {
	if conpty.IsConPtyAvailable() {
		c, err := conpty.Start(
			`cmd.exe /K chcp 65001>nul`,
			conpty.ConPtyWorkDir(homeDir()),
			conpty.ConPtyDimensions(120, 30),
		)
		if err == nil {
			return &conptyShell{p: c}, nil
		}
	}
	return startPipeShell()
}

// conptyShell 把第三方 ConPty 适配为 Shell 接口
type conptyShell struct{ p *conpty.ConPty }

func (s *conptyShell) Read(p []byte) (int, error)  { return s.p.Read(p) }
func (s *conptyShell) Write(p []byte) (int, error) { return s.p.Write(p) }
func (s *conptyShell) Resize(cols, rows int)       { _ = s.p.Resize(cols, rows) }
func (s *conptyShell) Close() error                { return s.p.Close() }

// startPipeShell ConPTY 不可用时的降级方案：一个常驻 cmd.exe 进程 + 管道
func startPipeShell() (Shell, error) {
	s, err := newPipeCmd("cmd.exe")
	if err != nil {
		return nil, err
	}
	// 统一 UTF-8 代码页
	io.WriteString(s.in, "chcp 65001>nul\r\n")
	return s, nil
}
