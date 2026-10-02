package core

// occupancy：每台 bot 的「占用 + 预定排队」模型。
//
// 一台机器同一时刻只允许一个「占用者」输入，其余接入者按 FIFO 排队；
// 观察者（Web 角色 1）只读旁听，既不占用也不排队。三类接入者都能收到终端输出。

import "time"

// SubKind 订阅者的接入方式
type SubKind int

const (
	SubLocal   SubKind = iota // 本机 DOS 终端窗口 / 内嵌终端（无账号）
	SubBrowser                // 浏览器控制台（有账号）
)

// ControlMode 订阅者对该机器的控制权状态
type ControlMode int

const (
	ModeObserver ControlMode = iota // 只读旁观，不入队
	ModeWaiting                     // 已排队，等前面的人释放
	ModeOperator                    // 当前占用者，可以输入
)

// Name 控制权状态的对外字符串（operator / waiting / observer）
func (m ControlMode) Name() string {
	switch m {
	case ModeOperator:
		return "operator"
	case ModeWaiting:
		return "waiting"
	default:
		return "observer"
	}
}

// Subscriber 一个终端接入者的身份
type Subscriber struct {
	Kind     SubKind
	UserID   int64
	Username string // 去重键；本机终端为空
	Display  string // 展示名；本机终端固定「本机终端」
	ReadOnly bool   // true = 只读旁观（Web 观察者），既不占用也不排队
}

// same 判断两个订阅者是否属于同一个人：浏览器按账号，本机终端不区分具体窗口。
func (s Subscriber) same(o Subscriber) bool {
	if s.Kind != o.Kind {
		return false
	}
	if s.Kind == SubLocal {
		return true
	}
	return s.UserID != 0 && s.UserID == o.UserID
}

// 控制消息类型（只推给单个订阅者，不广播）
const (
	CtrlGranted    = "granted"     // 轮到你，已获得控制权
	CtrlQueued     = "queued"      // 已被他人占用，已在队列中
	CtrlIdlePrompt = "idle_prompt" // 长时间无操作，询问是否下线
	CtrlRevoked    = "revoked"     // 控制权已被收回
)

// ControlMsg 定向推送给单个订阅者的控制消息
type ControlMsg struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Queue    int    `json:"queue"`    // 排队位次（1 起），非排队为 0
	Holder   string `json:"holder"`   // 当前占用者的展示名
	Deadline int64  `json:"deadline"` // idle_prompt 的截止时间（Unix 秒），其余为 0
}

// Occupancy 一台机器的占用概况（随机器列表下发给前端）
type Occupancy struct {
	Occupied  bool     `json:"occupied"`
	Text      string   `json:"text"` // 无人占用 / 张三 占用 / 张三、李四 占用 / 张三 等 3 人占用
	Occupants []string `json:"occupants"`
	Observers int      `json:"observers"` // 只读旁观人数
	Queue     int      `json:"queue"`     // 排队人数
}

// Reservation 当前用户在某一台机器上的预定情况
type Reservation struct {
	BotID        string `json:"bot_id"`
	BotName      string `json:"bot_name"`
	Mode         string `json:"mode"` // operator | waiting
	Queue        int    `json:"queue"`
	Holder       string `json:"holder"`
	IdleDeadline int64  `json:"idle_deadline"` // 0 = 没有正在进行的下线询问
}

// deadlineUnix 把询问截止时间转成下发给前端的 Unix 秒（零值给 0）
func deadlineUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
