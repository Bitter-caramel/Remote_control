//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// openAttachWindow 在新的 DOS 窗口中打开指定 bot 的专属终端：
//
//	cmd /c start "RemoteAssist - <botID>" "<本程序>" -attach <botID> -addr <ipc> -token <token>
//
// 主窗口立即返回继续可用，可同时为多台机器各开一个窗口。
func openAttachWindow(botID, ipcAddr, token string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	title := "RemoteAssist - " + botID
	cmd := exec.Command("cmd.exe", "/c", "start", title, exe,
		"-attach", botID, "-addr", ipcAddr, "-token", token)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动终端窗口失败: %w", err)
	}
	// 不 Wait：cmd 负责拉起新窗口后立即退出，终端窗口是独立进程
	return nil
}
