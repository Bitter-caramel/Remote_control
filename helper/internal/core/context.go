package core

import (
	"fmt"
	"sort"
)

// context.go：每台 bot 上的「操作上下文」层。
//
// 一条操作上下文 = 一个用户在一条机器上的一条独立操作线，对应被控端一个独立
// shell 进程。同一台机器上可以同时存在多条（上限由被控端把关），彼此不可见、
// 互不影响；跨用户观看或接续需要 owner 授权（授权是 DB 里的数据，core 不管，
// webui 判完把 owner / operate / watch 的结论传进来）。
//
// core 只负责三件事：
//  1. 输出路由：owner 与 operate 实时收；watch 只在「段」结束（一条命令跑完）时收；
//  2. 段缓冲：每条上下文保留最近 64KB 已完成的段，供新接入者回放；
//  3. 输入锁：同一上下文同一时刻只有一个输入持有者，防止多人同时打字互相打架。
const (
	// outHistoryLimit 每条上下文保留的回放上限（字节）
	outHistoryLimit = 64 * 1024
	// ctrlBuf 每个订阅者的控制消息缓冲（非阻塞投递，满了丢弃）
	ctrlBuf = 8
	// subOutBuf 每个订阅者的输出缓冲
	subOutBuf = 256

	// LocalCtxID 本机 DOS 终端 / 内嵌面板使用的保留上下文（它们没有账号体系）
	LocalCtxID = "local"
	// LocalOwnerName 保留上下文的展示名
	LocalOwnerName = "本机终端"
)

// subEntry 一条上下文上的一个订阅者
type subEntry struct {
	id   int
	ch   chan []byte
	ctrl chan ControlMsg
	who  Subscriber
	role string // SubOwner | SubOperate | SubWatch
}

// push 非阻塞投递一条控制消息：缓冲满就丢弃。
// 调用方必须持有 BOT.ctxMu，这样与 close(ch) 不会并发。
func (e *subEntry) push(msg ControlMsg) {
	select {
	case e.ctrl <- msg:
	default:
	}
}

// Subscription 一次上下文接入的句柄
type Subscription struct {
	Out       <-chan []byte     // 终端输出
	Ctrl      <-chan ControlMsg // 定向控制消息
	Role      string            // owner | operate | watch
	CtxID     string
	OwnerName string
	Input     bool // 接入时是否持有输入锁

	id  int
	bot *BOT
}

// Close 断开订阅。输入锁按「用户身份」而非连接持有，因此断开不自动释放
// （否则断线重连的人会莫名丢掉操作权），要释放请走 ReleaseInput。
func (s *Subscription) Close() { s.bot.unsubscribeCtx(s.id) }

// CtxView 一条上下文的对外摘要（账号、订阅人数、当前输入持有者）
type CtxView struct {
	ID        string `json:"id"`
	OwnerID   int64  `json:"owner_id"`
	OwnerName string `json:"owner_name"`
	Subs      int    `json:"subs"`
	InputName string `json:"input_name"` // 当前输入持有者展示名，空 = 无人持有
}

// CtxState 一条上下文的运行时状态。
// 全部字段由 BOT.ctxMu 保护（订阅通道的发送与关闭也在锁内，避免向已关闭通道写入）。
type CtxState struct {
	ID        string
	OwnerID   int64
	OwnerName string

	subs    map[int]*subEntry
	nextSub int

	curSeg []byte   // 当前段（尚未收到段结束标记的输出）
	segs   [][]byte // 已完成的段
	seq    int      // 段序号（以被控端回传的为准）

	inputHolder string // 输入锁持有者的 key（见 subKey），空 = 无人持有
	inputName   string // 输入锁持有者的展示名（用于提示文案）
}

// subKey 订阅者在输入锁里的身份键：
// 浏览器按账号 ID，本机终端统一用 "local"（无账号体系）。
func subKey(who Subscriber) string {
	if who.Kind == SubLocal {
		return "local"
	}
	return fmt.Sprintf("u%d", who.UserID)
}

// CtxCount 当前内存里的上下文数量（机器列表展示用）
func (b *BOT) CtxCount() int {
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	return len(b.ctxs)
}

// CtxViews 全部上下文的摘要（按创建顺序稳定排序）
func (b *BOT) CtxViews() []CtxView {
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	out := make([]CtxView, 0, len(b.ctxs))
	for _, c := range b.ctxs {
		out = append(out, CtxView{
			ID:        c.ID,
			OwnerID:   c.OwnerID,
			OwnerName: c.OwnerName,
			Subs:      len(c.subs),
			InputName: c.inputName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EnsureCtx 确保内存里有该上下文的状态（幂等）。
// webui 打开上下文时带着 owner 信息调用；被控端回传的「遗留上下文」也会惰性建出来，
// 此时 owner 未知，等 owner 真正接入时再补上。
func (b *BOT) EnsureCtx(ctxID string, ownerID int64, ownerName string) {
	if ctxID == "" {
		return
	}
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	c := b.ctxLocked(ctxID)
	if ownerID != 0 && c.OwnerID == 0 {
		c.OwnerID = ownerID
		c.OwnerName = ownerName
		if c.inputHolder == "" {
			// owner 默认持有输入锁
			c.inputHolder = fmt.Sprintf("u%d", ownerID)
			c.inputName = ownerName
		}
	}
}

// DropCtx 被控端报告某条上下文已终止（shell 退出）：销毁内存状态并关闭订阅通道。
func (b *BOT) DropCtx(ctxID string, reason string) {
	b.ctxMu.Lock()
	c, ok := b.ctxs[ctxID]
	if !ok {
		b.ctxMu.Unlock()
		return
	}
	delete(b.ctxs, ctxID)
	var pending []pendingEvent
	for id, e := range c.subs {
		delete(c.subs, id)
		close(e.ch)
		pending = append(pending, pendingEvent{EventRelease, e.who, "上下文已结束"})
	}
	b.ctxMu.Unlock()
	b.recordAll(pending)
	if reason != "" {
		b.Log().Sys("上下文 %s 结束: %s", ctxID, reason)
	}
}

// NotifyCtxOpened 被控端回报上下文开启结果（errMsg 非空表示失败），
// 转发给该上下文的所有订阅者，让等待中的浏览器终端给出明确反馈。
func (b *BOT) NotifyCtxOpened(ctxID, errMsg string) {
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	c, ok := b.ctxs[ctxID]
	if !ok {
		return
	}
	msg := ControlMsg{Type: CtrlOpened, Text: "终端已就绪"}
	if errMsg != "" {
		msg = ControlMsg{Type: CtrlFailed, Text: errMsg}
	}
	for _, e := range c.subs {
		e.push(msg)
	}
}

// SubscribeCtx 接入一条上下文：
//   - owner / operate：实时输出，接入时回放「已完成段 + 当前段」；
//   - watch：只读，按段同步，接入时只回放已完成的段。
func (b *BOT) SubscribeCtx(ctxID string, who Subscriber, role string) *Subscription {
	if who.Display == "" {
		who.Display = who.Username
	}
	if who.Display == "" {
		who.Display = "未知"
	}
	if !realtime(role) {
		role = SubWatch
	}

	b.ctxMu.Lock()
	c := b.ctxLocked(ctxID)
	id := c.nextSub
	c.nextSub++
	e := &subEntry{
		id:   id,
		ch:   make(chan []byte, subOutBuf),
		ctrl: make(chan ControlMsg, ctrlBuf),
		who:  who,
		role: role,
	}
	c.subs[id] = e
	// 输入的独占性：锁空着时，第一个可操作的接入者（owner / operate）直接持有；
	// 本机保留上下文与「owner 尚未来得及建锁」的场景都靠这条兜底。
	if realtime(role) && c.inputHolder == "" {
		c.inputHolder = subKey(who)
		c.inputName = who.Display
	}
	replay := c.replayLocked(role)
	ownerName := c.OwnerName
	holdsInput := c.inputHolder == subKey(who)
	b.ctxMu.Unlock()

	sub := &Subscription{
		Out:       e.ch,
		Ctrl:      e.ctrl,
		Role:      role,
		CtxID:     ctxID,
		OwnerName: ownerName,
		Input:     holdsInput,
		id:        id,
		bot:       b,
	}
	if len(replay) > 0 {
		select {
		case e.ch <- replay:
		default:
		}
	}
	b.recordAll([]pendingEvent{{EventOccupy, who, "接入上下文"}})
	return sub
}

// unsubscribeCtx 注销一个订阅者。输入锁按用户身份持有，断开连接不释放。
func (b *BOT) unsubscribeCtx(id int) {
	b.ctxMu.Lock()
	for _, c := range b.ctxs {
		e, ok := c.subs[id]
		if !ok {
			continue
		}
		delete(c.subs, id)
		close(e.ch)
		b.ctxMu.Unlock()
		b.recordAll([]pendingEvent{{EventRelease, e.who, "离开上下文"}})
		return
	}
	b.ctxMu.Unlock()
}

// CloseAllSubs 关闭全部上下文的订阅通道（机器下线时调用，让桥接协程自然退出）
func (b *BOT) CloseAllSubs(reason string) {
	b.ctxMu.Lock()
	var pending []pendingEvent
	for ctxID, c := range b.ctxs {
		for id, e := range c.subs {
			delete(c.subs, id)
			close(e.ch)
			detail := "离开上下文"
			if reason != "" {
				detail = "离开上下文（" + reason + "）"
			}
			pending = append(pending, pendingEvent{EventRelease, e.who, detail})
		}
		delete(b.ctxs, ctxID)
	}
	b.ctxMu.Unlock()
	b.recordAll(pending)
}

// PushCtxOut 接收某条上下文的输出：累积进当前段，并实时推给 owner / operate。
// 观看者（watch）要等段结束才拿到内容。
func (b *BOT) PushCtxOut(ctxID string, p []byte) {
	if ctxID == "" || len(p) == 0 {
		return
	}
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	c := b.ctxLocked(ctxID)
	c.curSeg = append(c.curSeg, p...)
	if len(c.curSeg) > outHistoryLimit {
		c.curSeg = append([]byte(nil), c.curSeg[len(c.curSeg)-outHistoryLimit:]...)
	}
	for _, e := range c.subs {
		if realtime(e.role) {
			select {
			case e.ch <- p:
			default:
			}
		}
	}
}

// PushCtxSegEnd 一段操作结束（被控端命中提示符哨兵，或静默兜底触发）：
// 把当前段封存进回放缓冲，并推给所有观看者。
func (b *BOT) PushCtxSegEnd(ctxID string, seq int) {
	b.ctxMu.Lock()
	defer b.ctxMu.Unlock()
	c := b.ctxLocked(ctxID)
	if seq > 0 {
		c.seq = seq
	} else {
		c.seq++
	}
	seg := c.curSeg
	c.curSeg = nil
	if len(seg) == 0 {
		return
	}
	c.segs = append(c.segs, seg)
	c.trimLocked()
	for _, e := range c.subs {
		if e.role == SubWatch {
			select {
			case e.ch <- seg:
			default:
			}
		}
	}
}

// TakeInput 尝试取得某条上下文的输入锁。成功时通知原持有者被接续。
func (b *BOT) TakeInput(ctxID string, who Subscriber) bool {
	key := subKey(who)
	b.ctxMu.Lock()
	c, ok := b.ctxs[ctxID]
	if !ok || c.inputHolder == key {
		b.ctxMu.Unlock()
		return ok
	}
	var revoked *subEntry
	prevName := c.inputName
	if old := c.inputHolder; old != "" {
		for _, e := range c.subs {
			if subKey(e.who) == old {
				revoked = e
				break
			}
		}
	}
	c.inputHolder = key
	c.inputName = who.Display
	if revoked != nil {
		// 失去输入权的非所有者降为只读（改按段同步）；owner 始终实时看自己的上下文
		if revoked.role == SubOperate {
			revoked.role = SubWatch
		}
		revoked.push(ControlMsg{
			Type:   CtrlRevoked,
			Text:   who.Display + " 已接续操作，你已转为只读",
			Holder: who.Display,
		})
	}
	var granted *subEntry
	for _, e := range c.subs {
		if subKey(e.who) == key {
			granted = e
			break
		}
	}
	if granted != nil {
		// 拿到输入权的观看者升为可操作：否则其角色仍是 watch，实时输出推不到它、
		// 服务端的输入闸门也会把它的按键拦掉（前端不会重连）。
		if granted.role == SubWatch {
			granted.role = SubOperate
		}
		granted.push(ControlMsg{Type: CtrlGranted, Text: "你已获得输入权", Holder: who.Display})
	}
	b.ctxMu.Unlock()

	if revoked != nil || granted != nil {
		detail := "取得输入权"
		if prevName != "" {
			detail = "接续 " + prevName + " 的操作"
		}
		b.recordAll([]pendingEvent{{EventOccupy, who, detail}})
	}
	return true
}

// ReleaseInput 释放输入锁（owner 收回 / 持有者主动放弃）。只有持有者自己或
// webui 判定 owner 收回时才会调用；key 不匹配则忽略。
func (b *BOT) ReleaseInput(ctxID string, who Subscriber) {
	key := subKey(who)
	b.ctxMu.Lock()
	c, ok := b.ctxs[ctxID]
	if !ok || c.inputHolder != key {
		b.ctxMu.Unlock()
		return
	}
	c.inputHolder = ""
	c.inputName = ""
	for _, e := range c.subs {
		if subKey(e.who) == key {
			// 主动放弃输入权的非所有者退回只读
			if e.role == SubOperate {
				e.role = SubWatch
			}
			e.push(ControlMsg{Type: CtrlRevoked, Text: "输入权已释放"})
		}
	}
	// 输入权回到 owner（若 owner 在线）
	if c.OwnerID != 0 {
		c.inputHolder = fmt.Sprintf("u%d", c.OwnerID)
		c.inputName = c.OwnerName
		for _, e := range c.subs {
			if subKey(e.who) == c.inputHolder {
				e.push(ControlMsg{Type: CtrlGranted, Text: "输入权已回到你手上", Holder: c.OwnerName})
			}
		}
	}
	b.ctxMu.Unlock()
	b.recordAll([]pendingEvent{{EventRelease, who, "释放输入权"}})
}

// RevokeGrantee 收回某人的输入锁（webui 撤销 operate 授权时调用），
// 锁立即回到 owner。返回是否确实收回了。
func (b *BOT) RevokeGrantee(ctxID string, granteeID int64) bool {
	key := fmt.Sprintf("u%d", granteeID)
	b.ctxMu.Lock()
	c, ok := b.ctxs[ctxID]
	if !ok || c.inputHolder != key {
		b.ctxMu.Unlock()
		return false
	}
	c.inputHolder = ""
	c.inputName = ""
	if c.OwnerID != 0 {
		c.inputHolder = fmt.Sprintf("u%d", c.OwnerID)
		c.inputName = c.OwnerName
	}
	for _, e := range c.subs {
		switch subKey(e.who) {
		case key:
			// 被收回授权的接续者退回只读（不再实时收输出）
			if e.role == SubOperate {
				e.role = SubWatch
			}
			e.push(ControlMsg{Type: CtrlRevoked, Text: "输入权已被收回", Holder: c.inputName})
		default:
			if subKey(e.who) == c.inputHolder {
				e.push(ControlMsg{Type: CtrlGranted, Text: "输入权已回到你手上", Holder: c.inputName})
			}
		}
	}
	b.ctxMu.Unlock()
	return true
}

// ---------- 内部小工具（调用方需持有 ctxMu） ----------

// ctxLocked 取出上下文状态，不存在则惰性创建
func (b *BOT) ctxLocked(ctxID string) *CtxState {
	if c, ok := b.ctxs[ctxID]; ok {
		return c
	}
	c := &CtxState{ID: ctxID, subs: make(map[int]*subEntry)}
	b.ctxs[ctxID] = c
	return c
}

// replayLocked 组装接入时的回放内容：
// owner / operate 要「已完成段 + 当前段」（看到当前画面），watch 只要已完成段。
func (c *CtxState) replayLocked(role string) []byte {
	var buf []byte
	for _, s := range c.segs {
		buf = append(buf, s...)
	}
	if realtime(role) {
		buf = append(buf, c.curSeg...)
	}
	if len(buf) > outHistoryLimit {
		buf = append([]byte(nil), buf[len(buf)-outHistoryLimit:]...)
	}
	return buf
}

// trimLocked 裁剪回放缓冲：已完成段总量超过上限时从最早的段开始丢
func (c *CtxState) trimLocked() {
	total := 0
	for _, s := range c.segs {
		total += len(s)
	}
	for total > outHistoryLimit && len(c.segs) > 1 {
		total -= len(c.segs[0])
		c.segs = c.segs[1:]
	}
}
