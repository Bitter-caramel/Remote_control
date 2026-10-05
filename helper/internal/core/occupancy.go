package core

// occupancy：订阅者身份与输入外观的最小类型。
//
// 旧模型是「一台机器一个占用者 + FIFO 排队 + 输出广播给所有人」，已废弃：
// 现在每台机器上可同时存在多条互相隔离的操作上下文（见 context.go），
// 每条上下文各自只有一个「输入持有者」，其余接入者只读。
//
// 本文件只保留与「人 / 通道」有关的类型；路由、段缓冲与输入锁都在 context.go。

// SubKind 订阅者的接入方式
type SubKind int

const (
	SubLocal   SubKind = iota // 本机 DOS 终端窗口 / 内嵌终端（无账号）
	SubBrowser                // 浏览器控制台（有账号）
)

// ControlMode 订阅者相对某条上下文的输入能力
type ControlMode int

const (
	ModeObserver ControlMode = iota // 只读：不持有输入锁
	ModeOperator                    // 持有输入锁，可以输入
)

// Name 控制权状态的对外字符串（operator / observer）
func (m ControlMode) Name() string {
	if m == ModeOperator {
		return "operator"
	}
	return "observer"
}

// Subscriber 一个终端接入者的身份
type Subscriber struct {
	Kind     SubKind
	UserID   int64
	Username string // 去重键；本机终端为空
	Display  string // 展示名；本机终端固定「本机终端」
}

// 订阅模式：由 webui 查库判定后传入，core 本身不做权限判定。
// 只有权限判定（owner / 授权 / 默认可看）通过的人才会走到订阅这一步。
const (
	SubOwner   = "owner"   // 上下文所有者：实时输出，默认持有输入锁
	SubOperate = "operate" // 被授权接续：实时输出，获得输入锁
	SubWatch   = "watch"   // 被授权观看：只读，按段同步
)

// realtime 该模式是否实时接收输出（否则按「段」同步）
func realtime(role string) bool { return role == SubOwner || role == SubOperate }

// 控制消息类型（只推给该上下文下的订阅者）
const (
	CtrlGranted = "granted" // 输入权已移交给你
	CtrlRevoked = "revoked" // 输入权被收回或被他人接续，你转为只读
	CtrlOpened  = "opened"  // 被控端 shell 已就绪
	CtrlFailed  = "failed"  // 上下文开启失败（Text 为原因，如超出配额）
)

// ControlMsg 定向推送给单个订阅者的控制消息
type ControlMsg struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Holder string `json:"holder"` // 当前输入持有者的展示名
}
