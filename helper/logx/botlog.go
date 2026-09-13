package logx

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// BotLog 单个 bot 的专属日志，按 botID 命名。
// 只记录该 bot 执行过的命令和对应回显（心跳不写入，心跳只用于检测连接）。
type BotLog struct {
	f *os.File
}

// NewBotLog 打开（不存在则创建）指定 botID 的专属日志文件
func NewBotLog(dir, botID string) (*BotLog, error) {
	if err := os.MkdirAll(dir+"/botlogs", 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(BotLogPath(dir, botID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &BotLog{f: f}, nil
}

// Sys 记录该 bot 的连接事件（上线/下线/面板接入等，心跳不记录）
func (b *BotLog) Sys(format string, v ...any) { b.write("[SYS] " + fmt.Sprintf(format, v...)) }

func (b *BotLog) write(line string) {
	fmt.Fprintf(b.f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

// WriteTranscript 追加终端原始字节流（PTY 模式）。
// 伪终端的输出里已包含命令回显、提示符与执行结果，相当于完整终端录像，
// 因此直接原样写入 botlog 即可完整留存"执行过的命令和对应回显"。
func (b *BotLog) WriteTranscript(p []byte) {
	b.f.Write(p)
}

// AgentCmd 记录 Agent/CLI 通道执行的一次性命令
func (b *BotLog) AgentCmd(cmd string) { b.write("[AGENT] CMD > " + cmd) }

// AgentOut 记录 Agent/CLI 命令的输出与退出码
func (b *BotLog) AgentOut(out string, code int) {
	body := strings.TrimRight(out, "\r\n")
	if body == "" {
		body = "(无输出)"
	}
	b.write(fmt.Sprintf("[AGENT] OUT < exit=%d %s", code, body))
}

// Close 关闭日志文件
func (b *BotLog) Close() error { return b.f.Close() }
