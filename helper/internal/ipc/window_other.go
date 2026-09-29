//go:build !windows

package ipc

import "errors"

// errNoNewWindow 非 Windows 平台无法弹出新的 DOS 窗口，面板回退到内嵌终端
var errNoNewWindow = errors.New("当前平台不支持弹出新窗口")

// OpenAttachWindow 非 Windows 平台不可用
func OpenAttachWindow(title, botID, ipcAddr, token string) error { return errNoNewWindow }
