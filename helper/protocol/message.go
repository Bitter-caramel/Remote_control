// Package protocol 定义协助者端与用户端共用的消息协议。
// 两端通信全部使用该统一结构（JSON 编码），避免两边各写一份对不上。
package protocol

import (
	"encoding/base64"
	"time"
)

// 消息类型常量
const (
	TypeRegister    = "register"     // 用户端→服务端：身份注册（首次上线 userID 为空，由服务端分配）
	TypeRegisterAck = "register_ack" // 服务端→用户端：确认/分配 userID
	TypeInput       = "input"        // 服务端→用户端：键盘输入的原始字节（写入远端终端）
	TypeOutput      = "output"       // 用户端→服务端：终端输出的原始字节
	TypeResize      = "resize"       // 服务端→用户端：终端窗口尺寸变化
	TypeHeartbeat   = "heartbeat"    // 用户端→服务端：心跳（只用于检测连接，不写任何日志）
	TypeBye         = "bye"          // 用户端→服务端：主动下线通知；IPC→终端窗口：机器已下线
	TypeAttach      = "attach"       // 终端窗口→主程序(本地IPC)：请求接入某 bot（UserID=botID, Data=令牌）
	TypeAttachAck   = "attach_ack"   // 主程序→终端窗口：接入结果（Data="ok" 或错误原因）
)

// HeartbeatInterval 用户端发送心跳的间隔
const HeartbeatInterval = 5 * time.Second

// HeartbeatPeriod 服务端心跳检测周期
const HeartbeatPeriod = 10 * time.Second

// MaxMissed 允许连续丢失心跳的周期数。
// 需求：1 个周期没收到 → 指针进缓存区列表继续等待；3 个周期没收到 → 销毁协程，机器下线。
const MaxMissed = 3

// Message 两端通信的统一消息结构。
// 终端是原始字节流（含 ANSI 控制序列，且可能在 UTF-8 多字节字符中间分包），
// 所以 input/output 的 Data 一律使用 base64 承载，保证二进制安全、不被 JSON 损坏。
type Message struct {
	Type   string `json:"type"`
	UserID string `json:"userID,omitempty"` // 注册时携带的身份标识（botID / userID）
	Data   string `json:"data,omitempty"`   // input/output：base64 编码的原始字节
	Cols   int    `json:"cols,omitempty"`   // resize：列数
	Rows   int    `json:"rows,omitempty"`   // resize：行数
}

// EncodeB64 把原始字节编码进消息字段
func EncodeB64(p []byte) string { return base64.StdEncoding.EncodeToString(p) }

// DecodeB64 还原消息字段中的原始字节
func DecodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
