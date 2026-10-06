package term

import (
	"fmt"
	"sync"
	"time"

	"remoteassist-user/protocol"
)

// session.go：多操作上下文（Ctx）管理器。
//
// 旧模型是一台机器一个 shell、所有人共用；新模型是「每个用户一条独立操作线」，
// 对应本机一个独立 shell 进程，各自 cwd / 环境 / 前台程序互不影响。
//
// 生命周期与连接解耦：断线不销毁会话（否则重连就丢 cwd 和正在跑的程序），
// 只在超过 sessionIdleTTL 没有任何活动时回收，避免 shell 进程泄漏。
const (
	// maxContextsPerBot 单台被控机上同时存在的上下文上限。
	// 每个 ConPTY 实例约占 20-30MB，必须设硬上限，防止被控端被拖垮。
	maxContextsPerBot = 5
	// sessionIdleTTL 无活动会话的保留时长，超过即回收
	sessionIdleTTL = 10 * time.Minute
	// segSilenceFallback 段静默兜底：输出静默超过该时长且未命中哨兵，也判定一段结束。
	// 用于 vim / top 这类长期不返回提示符的全屏程序，以及用户改掉 prompt 导致哨兵失效的情况。
	segSilenceFallback = 800 * time.Millisecond
	// fallbackTick 静默兜底的检查周期
	fallbackTick = 200 * time.Millisecond
	// gcInterval 空闲会话回收扫描周期
	gcInterval = time.Minute
	// sessBufSize 单次读取的缓冲大小
	sessBufSize = 8192
)

// session 一条操作上下文：一个独立 shell + 它的解析状态与段序号
type session struct {
	id     string
	shell  Shell // 创建后不再替换
	nonce  string
	parser *sentinelParser
	done   chan struct{} // 会话被回收时关闭，通知兜底协程退出

	mu         sync.Mutex
	cols       int
	rows       int
	seq        int       // 段序号，从 1 递增（重连时可由服务端下发基数续接）
	dirty      bool      // 自上一段结束以来是否有新输出（决定静默兜底是否要封段）
	lastOut    time.Time // 最近一次有输出的时间
	lastActive time.Time // 最近一次活动（读或写）时间，用于 GC
}

// nextSeg 递增段序号并清除未封段标记
func (s *session) nextSeg() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.dirty = false
	return s.seq
}

// SessionManager 一台被控机上的全部上下文。进程级单例：
// 在 agent.Run 里创建一次，重连时只切换 Sender，不重建会话。
type SessionManager struct {
	mu  sync.Mutex
	c   Sender // 当前连接的发送通道；断线期间为 nil，输出直接丢弃
	ses map[string]*session
}

// NewSessionManager 创建进程级上下文管理器并启动空闲回收协程
func NewSessionManager() *SessionManager {
	m := &SessionManager{ses: make(map[string]*session)}
	go m.gcLoop()
	return m
}

// SetSender 切换当前连接（连上时传入，断开时传 nil）
func (m *SessionManager) SetSender(s Sender) {
	m.mu.Lock()
	m.c = s
	m.mu.Unlock()
}

// send 把消息交给当前连接；断线期间静默丢弃
func (m *SessionManager) send(msg *protocol.Message) {
	m.mu.Lock()
	s := m.c
	m.mu.Unlock()
	if s == nil {
		return
	}
	_ = s.Send(msg)
}

// Open 开启或复用一条上下文：已存在则只同步尺寸，不存在则新建 shell 并注入提示符哨兵。
// cwd 是服务端从库里带下来的上次工作目录：bot 重启后 shell 已消失，用它在原目录重建；
// 为空或目录不存在则落到用户主目录。baseSeq 是服务端已落库的最大段序号，
// 新建 shell 时从它续接，保证重连前后的段序号单调（回放顺序才正确）。
// 复用已有会话时忽略 baseSeq（该会话自己的计数是对的）。
// 结果通过 TypeCtxOpened 回传（Err 非空表示失败）。
func (m *SessionManager) Open(ctxID string, cols, rows int, cwd string, baseSeq int) {
	if ctxID == "" {
		return
	}

	m.mu.Lock()
	if _, ok := m.ses[ctxID]; ok {
		m.mu.Unlock()
		m.Resize(ctxID, cols, rows)
		m.send(&protocol.Message{Type: protocol.TypeCtxOpened, CtxID: ctxID})
		return
	}
	if len(m.ses) >= maxContextsPerBot {
		m.mu.Unlock()
		m.send(&protocol.Message{
			Type:  protocol.TypeCtxOpened,
			CtxID: ctxID,
			Err:   fmt.Sprintf("上下文数量已达上限（%d）", maxContextsPerBot),
		})
		return
	}
	m.mu.Unlock()

	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 30
	}

	// 先定下随机 nonce：提示符哨兵要随 env 一起在 shell 启动时注入，
	// 之后再写 shell 会被回显成可见乱码（见 sentinel.go promptEnv）。
	nonce := newNonce()

	// 启动 shell 可能较慢，放在锁外
	sh, err := startShell(nonce, workDir(cwd))
	if err != nil {
		m.send(&protocol.Message{Type: protocol.TypeCtxOpened, CtxID: ctxID, Err: err.Error()})
		return
	}
	sh.Resize(cols, rows)
	if baseSeq < 0 {
		baseSeq = 0
	}
	s := &session{
		id:         ctxID,
		shell:      sh,
		nonce:      nonce,
		parser:     &sentinelParser{nonce: nonce},
		done:       make(chan struct{}),
		cols:       cols,
		rows:       rows,
		seq:        baseSeq, // 从服务端已落库的最大序号续接
		lastOut:    time.Now(),
		lastActive: time.Now(),
	}

	m.mu.Lock()
	if _, ok := m.ses[ctxID]; ok {
		// 并发下已被创建：保留先建的那条，丢弃刚启动的 shell
		m.mu.Unlock()
		_ = sh.Close()
		m.send(&protocol.Message{Type: protocol.TypeCtxOpened, CtxID: ctxID})
		return
	}
	m.ses[ctxID] = s
	m.mu.Unlock()

	go m.pump(s)
	go m.fallbackLoop(s)
	m.send(&protocol.Message{Type: protocol.TypeCtxOpened, CtxID: ctxID})
}

// Write 把输入写入指定上下文的 shell；上下文不存在时返回错误
func (m *SessionManager) Write(ctxID string, p []byte) error {
	s := m.get(ctxID)
	if s == nil {
		return fmt.Errorf("上下文 %s 不存在", ctxID)
	}
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
	_, err := s.shell.Write(p)
	return err
}

// Resize 调整指定上下文的窗口尺寸
func (m *SessionManager) Resize(ctxID string, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s := m.get(ctxID)
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	s.shell.Resize(cols, rows)
}

// CloseCtx 主动销毁一条上下文（回收 shell 进程）
func (m *SessionManager) CloseCtx(ctxID string) {
	if s := m.get(ctxID); s != nil {
		m.recycle(ctxID, s)
	}
}

// get 取出上下文（无则 nil）
func (m *SessionManager) get(ctxID string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ses[ctxID]
}

// recycle 从表中摘除并销毁会话；并发重复调用只会生效一次
func (m *SessionManager) recycle(ctxID string, s *session) {
	m.mu.Lock()
	cur, ok := m.ses[ctxID]
	if !ok || cur != s {
		m.mu.Unlock()
		return
	}
	delete(m.ses, ctxID)
	m.mu.Unlock()

	close(s.done)
	_ = s.shell.Close()
}

// emitSeg 封一段：段序号 +1，并把该段结束时的 cwd 一并回传（供服务端持久化、重连后恢复）。
func (m *SessionManager) emitSeg(s *session, cwd string) {
	msg := &protocol.Message{Type: protocol.TypeCtxSegEnd, CtxID: s.id, Seq: s.nextSeg()}
	if cwd != "" {
		msg.Data = protocol.EncodeB64([]byte(cwd))
	}
	m.send(msg)
}

// pump 持续读取一条上下文的 shell 输出：剥离哨兵 → 按流顺序下发字节与段边界。
// shell 退出即认为该上下文终止，回收并通知服务端。
func (m *SessionManager) pump(s *session) {
	buf := make([]byte, sessBufSize)
	for {
		n, err := s.shell.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			now := time.Now()
			s.mu.Lock()
			s.lastActive = now
			s.mu.Unlock()

			for _, ev := range s.parser.feed(data) {
				if ev.hit {
					// 命中哨兵：上一条命令跑完，封一段并带上当时的工作目录
					m.emitSeg(s, ev.cwd)
					continue
				}
				if len(ev.data) == 0 {
					continue
				}
				s.mu.Lock()
				s.dirty = true
				s.lastOut = now
				s.mu.Unlock()
				m.send(&protocol.Message{
					Type:  protocol.TypeOutput,
					CtxID: s.id,
					Data:  protocol.EncodeB64(ev.data),
				})
			}
		}
		if err != nil {
			m.recycle(s.id, s)
			m.send(&protocol.Message{Type: protocol.TypeCtxClosed, CtxID: s.id})
			return
		}
	}
}

// fallbackLoop 段静默兜底：全屏程序（vim/top）不会回到提示符，
// 哨兵迟迟不出现，此时按「输出静默」判定一段结束。
func (m *SessionManager) fallbackLoop(s *session) {
	t := time.NewTicker(fallbackTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			fire := s.dirty && time.Since(s.lastOut) >= segSilenceFallback
			s.mu.Unlock()
			if fire {
				// 静默兜底封段：没命中哨兵（全屏程序，或用户改过提示符导致
				// 哨兵失效）。这里**不再**向运行中的 shell 重注入提示符命令：
				// 写进去会被行规程回显成可见文本，还会和用户正在输入的命令
				// 撞车（实测出现过 `cd set PROMPT=...` 报错）。cwd 在此类段
				// 上取不到，属可接受损失，段边界本身仍靠静默判定。
				m.emitSeg(s, "")
			}
		}
	}
}

// gcLoop 周期回收长期无活动的会话（例如被控端一直在线但用户早已离线）
func (m *SessionManager) gcLoop() {
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		all := make([]*session, 0, len(m.ses))
		for _, s := range m.ses {
			all = append(all, s)
		}
		m.mu.Unlock()

		now := time.Now()
		for _, s := range all {
			s.mu.Lock()
			idle := now.Sub(s.lastActive) > sessionIdleTTL
			id := s.id
			s.mu.Unlock()
			if idle {
				m.CloseCtx(id)
			}
		}
	}
}
