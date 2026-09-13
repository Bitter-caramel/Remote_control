package agent

// Agent/CLI 的一次性命令执行通道。
//
// 与交互式 PTY（shell.go）完全独立：每次调用启动一个短命 shell 进程执行命令、
// 收集完整输出后退出。这样 Agent 跑命令不会干扰用户/协助者正打开的交互终端窗口，
// 也不会改变交互 shell 的当前目录和环境。

import (
	"context"
	"os/exec"
	"runtime"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var gbkDecoder = simplifiedchinese.GBK.NewDecoder()

// execOneShot 执行一条命令，返回 UTF-8 输出和退出码。
// timeoutSec<=0 时使用默认值；超过最大值时截断到最大值。
// 超时或无法启动时退出码为 -1。
func execOneShot(cmdLine string, timeoutSec int) (string, int) {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	if timeoutSec > 600 {
		timeoutSec = 600
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		// 在同一 cmd 里先切 UTF-8 代码页，再执行命令；退出码取最后一条命令的
		c = exec.CommandContext(ctx, "cmd", "/c", "chcp 65001>nul & "+cmdLine)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmdLine)
	}
	c.Dir = homeDir()

	raw, err := c.CombinedOutput()
	out := decodeExecOutput(raw)

	if ctx.Err() == context.DeadlineExceeded {
		return out + "\n[执行超时，已终止]", -1
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, ee.ExitCode()
		}
		return out + "\n[启动失败] " + err.Error(), -1
	}
	return out, 0
}

// decodeExecOutput 把命令输出归一化为 UTF-8：
// 合法 UTF-8 原样返回；中文系统(代码页 936)上的非 UTF-8 输出按 GBK 解码；其余原样。
func decodeExecOutput(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	if runtime.GOOS == "windows" && ansicp936() {
		if decoded, err := gbkDecoder.Bytes(raw); err == nil {
			return string(decoded)
		}
	}
	return string(raw)
}
