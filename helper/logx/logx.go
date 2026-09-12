// Package logx 协助者端的三类日志：
//   - programlog：程序运行日志（正确 + 错误）
//   - botlog：每个 bot 一个专属日志，记录执行过的命令和回显（心跳不写入）
//   - botslog：记录所有连接过的主机，以及对应的 botlog 文件名，便于查找
package logx

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// ProgramLog 程序运行日志
type ProgramLog struct {
	path string
	f    *os.File
	l    *log.Logger
}

// NewProgramLog 打开（不存在则创建）program.log
func NewProgramLog(dir string) (*ProgramLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "program.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &ProgramLog{path: path, f: f, l: log.New(f, "", log.LstdFlags)}, nil
}

// Info 记录正确日志
func (p *ProgramLog) Info(format string, v ...any) { p.l.Printf("[INFO] "+format, v...) }

// Error 记录错误日志
func (p *ProgramLog) Error(format string, v ...any) { p.l.Printf("[ERROR] "+format, v...) }

// Path 返回日志文件路径
func (p *ProgramLog) Path() string { return p.path }

// Close 关闭日志文件
func (p *ProgramLog) Close() error { return p.f.Close() }

// Read 返回全部日志内容（log -p 用）
func (p *ProgramLog) Read() (string, error) {
	b, err := os.ReadFile(p.path)
	if err != nil {
		return "", fmt.Errorf("读取 programlog 失败: %w", err)
	}
	return string(b), nil
}

// BotsLog 记录所有连接过的主机及其 botlog 文件名
type BotsLog struct {
	path string
}

// NewBotsLog 初始化 bots.log 路径（文件在首次写入时创建）
func NewBotsLog(dir string) *BotsLog {
	return &BotsLog{path: filepath.Join(dir, "bots.log")}
}

// Add 追加一条上线记录：时间 | botID | 地址 | botlog 文件名
func (b *BotsLog) Add(botID, addr string) {
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s | %-10s | %-21s | botlogs/%s.log\n",
		time.Now().Format("2006-01-02 15:04:05"), botID, addr, botID)
}

// Read 返回全部内容（log --bots / bots -a 用）
func (b *BotsLog) Read() (string, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "(botslog 为空，还没有机器连接过)\n", nil
		}
		return "", fmt.Errorf("读取 botslog 失败: %w", err)
	}
	return string(data), nil
}

// BotLogPath 返回指定 botID 的 botlog 文件路径
func BotLogPath(dir, botID string) string {
	return filepath.Join(dir, "botlogs", botID+".log")
}

// ReadBotLog 读取指定 botID 的 botlog（log --botlog+ID 用）
func ReadBotLog(dir, botID string) (string, error) {
	data, err := os.ReadFile(BotLogPath(dir, botID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("找不到 %s 的 botlog", botID)
		}
		return "", err
	}
	return string(data), nil
}
