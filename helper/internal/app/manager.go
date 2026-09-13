package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// Manager 主程序中的 bots 管理器：保存每个在线 bot 的指针（bots 列表），
// 以及疑似掉线机器的指针（缓存区列表）。所有操作并发安全。
type Manager struct {
	mu       sync.Mutex
	bots     map[string]*BOT // bots 列表：在线机器
	buffered map[string]*BOT // 缓存区列表：1 个周期没收到心跳、还在等待的机器

	execMu   sync.Mutex
	execWait map[string]execWaiter // exec 请求待回包（MsgID→等待者）

	logDir  string
	prog    *logx.ProgramLog
	botslog *logx.BotsLog
}

func NewManager(logDir string, prog *logx.ProgramLog, botslog *logx.BotsLog) *Manager {
	return &Manager{
		bots:     make(map[string]*BOT),
		buffered: make(map[string]*BOT),
		execWait: make(map[string]execWaiter),
		logDir:   logDir,
		prog:     prog,
		botslog:  botslog,
	}
}

// AllocID 给首次上线的机器分配一个不重复的 botID
func (m *Manager) AllocID() string {
	for {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			panic(err) // 系统随机源不可用，属于致命错误
		}
		id := "B" + hex.EncodeToString(buf)
		if _, exists := m.bots[id]; exists {
			continue
		}
		if _, err := os.Stat(logx.BotLogPath(m.logDir, id)); err == nil {
			continue // 与历史 botlog 撞名，重新分配
		}
		return id
	}
}

// Register 机器上线：写入 bots 列表并记录 botslog。
// 若同 ID 的旧连接还挂着（比如掉线后还没到 3 个周期就重连了），先清掉旧对象。
func (m *Manager) Register(b *BOT) {
	m.mu.Lock()
	old := m.bots[b.ID]
	delete(m.bots, b.ID)
	delete(m.buffered, b.ID)
	m.bots[b.ID] = b
	m.mu.Unlock()

	if old != nil {
		old.Conn.Close()
		old.blog.Sys("被同 ID 的新连接取代")
		old.blog.Close()
	}
	m.botslog.Add(b.ID, b.NameOrDash(), b.OSOrDash(), b.Addr()) // botslog 记录连接过的主机及对应 botlog 文件名
}

// Remove 机器下线：销毁对应协程管理的连接，bots 列表和缓存区列表都删除指针
func (m *Manager) Remove(id, reason string) {
	m.mu.Lock()
	b, ok := m.bots[id]
	delete(m.bots, id)
	delete(m.buffered, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	b.Conn.Close()
	m.failExecsOfBot(id) // 唤醒等待该 bot 执行结果的 CLI
	b.blog.Sys("下线: %s", reason)
	b.blog.Close()
	fmt.Printf("[%s] 机器下线: %s | 名称: %s | 地址: %s — %s\n",
		time.Now().Format("15:04:05"), id, b.NameOrDash(), b.Addr(), reason)
	m.prog.Info("bot %s 下线 (%s): %s", id, reason, b.Addr())
}

// Get 按 botID 查找在线 bot
func (m *Manager) Get(id string) *BOT {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bots[id]
}

// Count 在线数量
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bots)
}

// Snapshot 返回在线 bots 列表的副本
func (m *Manager) Snapshot() []*BOT {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]*BOT, 0, len(m.bots))
	for _, b := range m.bots {
		list = append(list, b)
	}
	return list
}

// Missed 心跳检测：该周期内没收到心跳，丢失计数 +1。
// 第 1 次丢失 → 指针写入缓存区列表继续等待；达到 MaxMissed（3 个周期）→ 销毁，机器下线。
func (m *Manager) Miss(b *BOT) {
	m.mu.Lock()
	b.Missed++
	if !b.Buffered {
		b.Buffered = true
		m.buffered[b.ID] = b
	}
	first := b.Missed == 1
	destroy := b.Missed >= protocol.MaxMissed
	m.mu.Unlock()

	if first {
		fmt.Printf("[%s] 警告: %s (%s) %d 个周期未收到心跳，已写入缓存区列表，继续等待\n",
			time.Now().Format("15:04:05"), b.Title(), b.Addr(), b.Missed)
		b.blog.Sys("连续 %d 个周期未收到心跳，进入缓存区等待", b.Missed)
		m.prog.Info("bot %s 心跳丢失，进入缓存区", b.ID)
	}
	if destroy {
		m.Remove(b.ID, fmt.Sprintf("连续 %d 个检测周期未收到心跳", b.Missed))
	}
}

// Recover 心跳恢复：清零丢失计数并移出缓存区列表
func (m *Manager) Recover(b *BOT) {
	m.mu.Lock()
	b.Missed = 0
	was := b.Buffered
	b.Buffered = false
	delete(m.buffered, b.ID)
	m.mu.Unlock()
	if was {
		m.prog.Info("bot %s 心跳恢复，移出缓存区", b.ID)
	}
}
