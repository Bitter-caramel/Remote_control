//go:build !windows

package main

import "errors"

// errNoNewWindow 非 Windows 平台无法弹出新的 DOS 窗口，面板回退到内嵌终端
var errNoNewWindow = errors.New("当前平台不支持弹出新窗口")

func openAttachWindow(botID, ipcAddr, token string) error { return errNoNewWindow }
