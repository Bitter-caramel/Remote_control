package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// outHistoryLimit 每个 bot 保留的终端输出回放上限（字节）。
// 新接入的终端窗口/浏览器会先收到这段回放，接入瞬间就能看到终端的当前画面，
// 而不是对着空白屏幕猜远端的状态。
const outHistoryLimit = 64 * 1024

// ctrlBuf 每个订阅者的控制消息缓冲（非阻塞投递，满了丢弃）
const ctrlBuf = 8

// subEntry 一个终端订阅者
type subEntry struct {
	ch   chan []byte     // 终端输出（所有订阅者都收）
	ctrl chan ControlMsg // 定向控制消息（只推给这一条连接）
	who  Subscriber
	mode ControlMode
}

// push 非阻塞投递一条控制消息：缓冲满就丢弃。
// 调用方必须持有 BOT.subMu，这样与 close(ch) 不会并发。
func (e *subEntry) push(msg ControlMsg) {
	select {
	case e.ctrl <- msg:
	default:
	}
}

// Subscription 一次终端接入的句柄
type Subscription struct {
	Out    <-chan []byte     // 终端输出
	Ctrl   <-chan ControlMsg // 定向控制消息
	Mode   ControlMode       // 接入时刻的控制权状态
	Queue  int               // 排队位次（1 起），非排队为 0
	Holder string            // 接入时刻的占用者展示名（自己占用时就是自己）
	Close  func()            // 断开；若自己是占用者会触发让位

	id  int  // 自身订阅号（内部使用）
	bot *BOT // 所属机器
}

// AnswerIdle 应答「是否下线」询问：keep=true 表示点「是」（下线），false 表示点「否」（继续）
func (s *Subscription) AnswerIdle(keep bool) { s.bot.AnswerIdle(s.id, keep) }

// BOT 一个已连接的客户端（机器）对象。
// 每个 BOT 由一个独立协程（uplink 的读循环）服务，协程之间互不通信。
type BOT struct {
	ID   string // botID：首次上线由服务端分配，客户端保存到本地 config，之后靠它识别身份
	Name string // 被控机自定义名称（用户在 config.json 填写，可能为空）
	OS   string // 被控机系统信息（如 windows/amd64 (10.0.26200)）
	IP   string
	Port int

	Conn *websocket.Conn
	wmu  sync.Mutex // 保护 Conn 的并发写（命令下发、心跳确认等来自不同协程）

	LastBeat atomic.Int64 // 最近一次收到心跳的 UnixNano 时间戳
	Missed   int          // 连续未收到心跳的检测周期数
	Buffered bool         // 是否已进入缓存区列表

	OnlineAt time.Time // 上线时间（bots -l 用）

	// 终端接入者集合与控制权：同一时刻只有一个「占用者」能输入，
	// 其余按 FIFO 排队；观察者只读旁观，既不占用也不排队。三者都能收到终端输出。
	subMu   sync.Mutex
	subs    map[int]*subEntry
	nextSub int
	holder  int    // 当前占用者的订阅号，-1 表示空闲
	queue   []int  // 排队中的订阅号（FIFO）
	hist    []byte // 最近输出的回放缓冲（上限 outHistoryLimit 字节）

	// 闲置判定：「用户没有输入 且 机器也没有回传输出」才算无操作。
	// 时间戳在 Send(TypeInput) 与 PushOut 两处刷新，因此像装环境、装依赖这类
	// 长时间没有人工输入、但机器持续刷屏的任务不会被误判为闲置。
	lastActive   atomic.Int64 // 最近一次有操作的 UnixNano
	idleAsked    bool         // 是否已发出「是否下线」询问
	idleDeadline time.Time    // 询问的截止时间

	events *EventLog // 播报历史；由 Manager.Register 注入，可为 nil

	blog *logx.BotLog // 该 bot 的专属日志
}

// NewBOT 构造 BOT 对象并打开其专属 botlog
func NewBOT(id, ip string, port int, conn *websocket.Conn, logDir string) (*BOT, error) {
	blog, err := logx.NewBotLog(logDir, id)
	if err != nil {
		return nil, err
	}
	b := &BOT{
		ID:       id,
		IP:       ip,
		Port:     port,
		Conn:     conn,
		OnlineAt: time.Now(),
		subs:     make(map[int]*subEntry),
		holder:   -1,
		blog:     blog,
	}
	b.LastBeat.Store(time.Now().UnixNano())
	b.Touch()
	return b, nil
}

// SetEventLog 注入播报历史（由 Manager.Register 在机器上线、尚无订阅者时调用）
func (b *BOT) SetEventLog(e *EventLog) { b.events = e }

// Log 该 bot 的专属日志（终端录像 + Agent 审计）
func (b *BOT) Log() *logx.BotLog { return b.blog }

// Send 并发安全地向该 bot 发送一条消息
func (b *BOT) Send(msg *protocol.Message) error {
	if msg.Type == protocol.TypeInput {
		b.Touch() // 用户输入 = 有操作，重置闲置计时
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return b.Conn.WriteJSON(msg)
}

// Addr 返回该 bot 的来源地址（IP:Port）
func (b *BOT) Addr() string { return fmt.Sprintf("%s:%d", b.IP, b.Port) }

// Title 带名称的显示标题：有自定义名称时为 "名称(botID)"，否则只有 botID
func (b *BOT) Title() string {
	if b.Name != "" {
		return b.Name + "(" + b.ID + ")"
	}
	return b.ID
}

// NameOrDash 名称列展示用：空名称显示 -
func (b *BOT) NameOrDash() string {
	if b.Name == "" {
		return "-"
	}
	return b.Name
}

// OSOrDash 系统列展示用：未知显示 -
func (b *BOT) OSOrDash() string {
	if b.OS == "" {
		return "-"
	}
	return b.OS
}

// Touch 刷新「最近一次有操作」的时间
func (b *BOT) Touch() { b.lastActive.Store(time.Now().UnixNano()) }

// Subscribe 注册一个本机终端订阅者（DOS 终端窗口 / 内嵌终端）。
// 本机终端同样要排队：协助者自己也可能撞上别的接入者正在操作。
func (b *BOT) Subscribe() *Subscription {
	return b.SubscribeAs(Subscriber{Kind: SubLocal, Display: "本机终端"})
}

// SubscribeAs 注册一个订阅者并仲裁控制权：
//   - 只读旁观者（Web 观察者）→ ModeObserver，不入队
//   - 机器空闲 → 立即成为占用者
//   - 机器已被占用 → 加入队尾，ModeWaiting
//
// 订阅瞬间会先收到一次历史回放。
func (b *BOT) SubscribeAs(who Subscriber) *Subscription {
	if who.Display == "" {
		who.Display = who.Username
	}
	if who.Display == "" {
		who.Display = "未知"
	}

	var (
		pending []pendingEvent
		sub     = &Subscription{}
	)

	b.subMu.Lock()
	id := b.nextSub
	b.nextSub++
	e := &subEntry{
		ch:   make(chan []byte, 256),
		ctrl: make(chan ControlMsg, ctrlBuf),
		who:  who,
	}
	switch {
	case who.ReadOnly:
		e.mode = ModeObserver
	case b.holder < 0:
		e.mode = ModeOperator
		b.holder = id
	case len(b.queue) > 0 && b.queueIndexLocked(id) >= 0:
		e.mode = ModeWaiting // 不会发生，纯防御
	default:
		e.mode = ModeWaiting
		b.queue = append(b.queue, id)
	}
	b.subs[id] = e
	if len(b.hist) > 0 {
		e.ch <- append([]byte(nil), b.hist...)
	}

	holderName := b.holderNameLocked()
	sub.Out = e.ch
	sub.Ctrl = e.ctrl
	sub.Mode = e.mode
	sub.Holder = holderName
	sub.Close = func() { b.unsubscribe(id) }
	sub.id = id
	sub.bot = b

	switch e.mode {
	case ModeOperator:
		b.lastActive.Store(time.Now().UnixNano())
		b.idleAsked = false
		b.idleDeadline = time.Time{}
		pending = append(pending, pendingEvent{EventOccupy, who, "占用"})
	case ModeWaiting:
		sub.Queue = b.queuePosLocked(id)
		e.push(ControlMsg{
			Type:   CtrlQueued,
			Text:   who.Display + " 之外的接入者正在操作，已进入队列",
			Queue:  sub.Queue,
			Holder: holderName,
		})
		pending = append(pending, pendingEvent{EventQueue, who, "排队"})
	}
	b.subMu.Unlock()

	b.recordAll(pending)
	return sub
}

// unsubscribe 注销一个订阅者；若退出的是占用者，则把控制权让给队首。
func (b *BOT) unsubscribe(id int) {
	b.subMu.Lock()
	e, ok := b.subs[id]
	if !ok {
		b.subMu.Unlock()
		return // 已经被 CloseSubs / RevokeHolder 清掉了，不重复记账
	}
	delete(b.subs, id)
	close(e.ch)

	var (
		pending  []pendingEvent
		promoted *subEntry
	)
	if b.holder == id {
		b.holder = -1
		b.idleAsked = false
		b.idleDeadline = time.Time{}
		pending = append(pending, pendingEvent{EventRelease, e.who, "释放"})
		promoted = b.promoteLocked()
	} else if i := b.queueIndexLocked(id); i >= 0 {
		b.queue = append(b.queue[:i], b.queue[i+1:]...)
	}
	if promoted != nil {
		promoted.push(ControlMsg{
			Type:   CtrlGranted,
			Text:   "轮到你了，已获得控制权",
			Holder: promoted.who.Display,
		})
		pending = append(pending, pendingEvent{EventOccupy, promoted.who, "占用"})
	}
	b.subMu.Unlock()

	b.recordAll(pending)
}

// CloseSubs 关闭全部订阅通道（机器下线时调用，让桥接协程自然退出）。
// 机器都下线了，不再让位给下一位。
func (b *BOT) CloseSubs(reason string) {
	b.subMu.Lock()
	var pending []pendingEvent
	for id, e := range b.subs {
		delete(b.subs, id)
		close(e.ch)
		if e.mode == ModeOperator {
			detail := "释放"
			if reason != "" {
				detail = "释放（" + reason + "）"
			}
			pending = append(pending, pendingEvent{EventRelease, e.who, detail})
		}
	}
	b.holder = -1
	b.queue = nil
	b.idleAsked = false
	b.idleDeadline = time.Time{}
	b.subMu.Unlock()

	b.recordAll(pending)
}

// PushOut 把终端输出广播给所有订阅者，并追加到回放缓冲。
// 订阅者来不及消费时直接丢弃该订阅者的这一份（内容已写入 botlog）。
func (b *BOT) PushOut(p []byte) {
	b.Touch() // 机器有输出 = 有操作，重置闲置计时

	b.subMu.Lock()
	defer b.subMu.Unlock()

	b.hist = append(b.hist, p...)
	if len(b.hist) > outHistoryLimit {
		b.hist = append([]byte(nil), b.hist[len(b.hist)-outHistoryLimit:]...)
	}
	for _, e := range b.subs {
		select {
		case e.ch <- p:
		default:
		}
	}
}

// ---------- 控制权 ----------

// RevokeHolder 收回当前占用者的控制权（闲置超时等），并把控制权让给队首。
func (b *BOT) RevokeHolder(reason string) { b.revokeHolder(-1, reason) }

// revokeHolder 收回控制权；expect >= 0 时只在该订阅号仍是占用者时才收回
// （避免「闲置询问的应答」误伤刚被提升上来的下一位）。
func (b *BOT) revokeHolder(expect int, reason string) {
	b.subMu.Lock()
	if b.holder < 0 || (expect >= 0 && b.holder != expect) {
		b.subMu.Unlock()
		return
	}
	e, ok := b.subs[b.holder]
	if !ok {
		b.holder = -1
		b.subMu.Unlock()
		return
	}
	who := e.who
	delete(b.subs, b.holder)
	e.push(ControlMsg{Type: CtrlRevoked, Text: "已收回控制权：" + reason})
	close(e.ch)
	b.holder = -1
	b.idleAsked = false
	b.idleDeadline = time.Time{}

	promoted := b.promoteLocked()
	if promoted != nil {
		promoted.push(ControlMsg{
			Type:   CtrlGranted,
			Text:   "轮到你了，已获得控制权",
			Holder: promoted.who.Display,
		})
	}

	pending := []pendingEvent{{EventRelease, who, "释放（" + reason + "）"}}
	if promoted != nil {
		pending = append(pending, pendingEvent{EventOccupy, promoted.who, "占用"})
	}
	b.subMu.Unlock()

	b.recordAll(pending)
}

// Release 主动放弃：关掉 who 在这台机器上的订阅
// （占用者让位给队首，排队者退出队列）。返回是否找到了这样一条订阅。
func (b *BOT) Release(who Subscriber) bool {
	b.subMu.Lock()
	target := -1
	for id, e := range b.subs {
		if e.who.same(who) {
			target = id
			break
		}
	}
	b.subMu.Unlock()
	if target < 0 {
		return false
	}
	b.unsubscribe(target)
	return true
}

// AnswerIdle 占用者回答「是否下线」：keep=true 表示点「是」（下线），false 表示点「否」（继续）。
func (b *BOT) AnswerIdle(id int, keep bool) {
	if keep {
		b.revokeHolder(id, "用户确认下线")
		return
	}
	b.subMu.Lock()
	if b.holder == id {
		b.idleAsked = false
		b.idleDeadline = time.Time{}
		b.lastActive.Store(time.Now().UnixNano()) // 重新计 5 分钟
	}
	b.subMu.Unlock()
}

// IdleState 闲置扫描用：当前占用者的订阅号、最近操作时间与询问状态。
// 没有占用者时 subID 为 -1。
func (b *BOT) IdleState() (subID int, lastActive time.Time, asked bool, deadline time.Time) {
	b.subMu.Lock()
	defer b.subMu.Unlock()
	if b.holder < 0 {
		return -1, time.Time{}, false, time.Time{}
	}
	if _, ok := b.subs[b.holder]; !ok {
		return -1, time.Time{}, false, time.Time{}
	}
	return b.holder, time.Unix(0, b.lastActive.Load()), b.idleAsked, b.idleDeadline
}

// HolderIsBrowser 当前占用者是否是浏览器接入者。
// 闲置下线只针对 Web 用户：本机 DOS 终端没有应答界面（也没有「下线」这个概念，
// 人被赶回主面板只会莫名其妙），因此不参与闲置询问。
func (b *BOT) HolderIsBrowser() bool {
	b.subMu.Lock()
	defer b.subMu.Unlock()
	e, ok := b.subs[b.holder]
	return ok && e.who.Kind == SubBrowser
}

// AskIdle 向当前占用者发出「是否下线」询问；已在询问中则返回 false。
func (b *BOT) AskIdle(deadline time.Time) bool {
	b.subMu.Lock()
	defer b.subMu.Unlock()
	e, ok := b.subs[b.holder]
	if !ok || b.holder < 0 || b.idleAsked {
		return false
	}
	b.idleAsked = true
	b.idleDeadline = deadline
	e.push(ControlMsg{
		Type:     CtrlIdlePrompt,
		Text:     "长时间没有操作，是否下线？",
		Deadline: deadline.Unix(),
	})
	return true
}

// Occupancy 当前占用概况（只读快照，锁内不做 IO）
func (b *BOT) Occupancy() Occupancy {
	b.subMu.Lock()
	defer b.subMu.Unlock()
	return b.occupancyLocked()
}

func (b *BOT) occupancyLocked() Occupancy {
	var occ Occupancy
	seen := make(map[string]bool)
	for _, e := range b.subs {
		switch e.mode {
		case ModeOperator:
			if !seen[e.who.Display] {
				seen[e.who.Display] = true
				occ.Occupants = append(occ.Occupants, e.who.Display)
			}
		case ModeObserver:
			occ.Observers++
		}
	}
	occ.Queue = len(b.queue)
	sort.Strings(occ.Occupants)
	occ.Occupied = len(occ.Occupants) > 0
	occ.Text = occupancyText(occ.Occupants)
	return occ
}

// occupancyText 只有四种：无人占用 / 单人 / 两人并列 / 三人以上取前两名计数
func occupancyText(names []string) string {
	switch len(names) {
	case 0:
		return "无人占用"
	case 1:
		return names[0] + " 占用"
	case 2:
		return names[0] + "、" + names[1] + " 占用"
	default:
		return fmt.Sprintf("%s 等 %d 人占用", names[0], len(names))
	}
}

// ReservationFor 指定订阅者在这台机器上的预定情况（没接入则 ok=false）
func (b *BOT) ReservationFor(who Subscriber) (Reservation, bool) {
	b.subMu.Lock()
	defer b.subMu.Unlock()

	holderName := b.holderNameLocked()
	mode := ""
	pos := 0
	for _, e := range b.subs {
		if e.mode == ModeOperator && e.who.same(who) {
			mode = "operator"
			break
		}
	}
	if mode == "" {
		for i, id := range b.queue {
			e, ok := b.subs[id]
			if !ok || !e.who.same(who) {
				continue
			}
			mode = "waiting"
			pos = i + 1
			break
		}
	}
	if mode == "" {
		return Reservation{}, false
	}
	return Reservation{
		BotID:        b.ID,
		BotName:      b.NameOrDash(),
		Mode:         mode,
		Queue:        pos,
		Holder:       holderName,
		IdleDeadline: deadlineUnix(b.idleDeadline),
	}, true
}

// ---------- 内部小工具（调用方需持有 subMu） ----------

func (b *BOT) holderNameLocked() string {
	if b.holder < 0 {
		return ""
	}
	if e, ok := b.subs[b.holder]; ok {
		return e.who.Display
	}
	return ""
}

func (b *BOT) queueIndexLocked(id int) int {
	for i, q := range b.queue {
		if q == id {
			return i
		}
	}
	return -1
}

func (b *BOT) queuePosLocked(id int) int {
	if i := b.queueIndexLocked(id); i >= 0 {
		return i + 1
	}
	return 0
}

// promoteLocked 让位：把队首还活着的可操作者提升为占用者
func (b *BOT) promoteLocked() *subEntry {
	for len(b.queue) > 0 {
		id := b.queue[0]
		b.queue = b.queue[1:]
		e, ok := b.subs[id]
		if !ok {
			continue // 条目已失效，跳过
		}
		e.mode = ModeOperator
		b.holder = id
		b.idleAsked = false
		b.idleDeadline = time.Time{}
		b.lastActive.Store(time.Now().UnixNano())
		return e
	}
	return nil
}

// pendingEvent 待写进播报历史的记录（锁内收集，锁外追加）
type pendingEvent struct {
	kind   EventKind
	who    Subscriber
	detail string
}

func (b *BOT) recordAll(evs []pendingEvent) {
	if b.events == nil || len(evs) == 0 {
		return
	}
	name := b.NameOrDash()
	for _, ev := range evs {
		b.events.Append(Event{
			Kind:     ev.kind,
			BotID:    b.ID,
			BotName:  name,
			Actor:    strings.TrimSpace(ev.who.Display),
			Username: ev.who.Username,
			Detail:   ev.detail,
		})
	}
}
