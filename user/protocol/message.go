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
	TypeInput       = "input"        // 服务端→用户端：键盘输入的原始字节（写入远端交互终端）
	TypeOutput      = "output"       // 用户端→服务端：交互终端输出的原始字节
	TypeResize      = "resize"       // 服务端→用户端：终端窗口尺寸变化
	TypeHeartbeat   = "heartbeat"    // 用户端→服务端：心跳（只用于检测连接，不写任何日志）
	TypeBye         = "bye"          // 用户端→服务端：主动下线通知；IPC→终端窗口：机器已下线

	TypeExec       = "exec"        // 服务端→用户端：执行一条一次性命令（与交互终端互不干扰），Data=命令文本
	TypeExecResult = "exec_result" // 用户端→服务端：一次性命令的输出（base64）与退出码

	TypeAttach    = "attach"     // 终端窗口→主程序(本地IPC)：请求接入某 bot（UserID=botID, Data=令牌）
	TypeAttachAck = "attach_ack" // 主程序→终端窗口：接入结果（Data="ok" 或错误原因）

	TypeCLIHello    = "cli_hello" // CLI/Agent→主程序：本地 CLI 握手（Data=令牌）
	TypeCLIHelloAck = "cli_hello_ack"
	TypeCLIList     = "cli_list"     // CLI→主程序：查询在线 bot 列表
	TypeCLIListAck  = "cli_list_ack" // 主程序→CLI：Data=JSON 数组
	TypeCLIExec     = "cli_exec"     // CLI→主程序：请求在某 bot 执行命令（UserID=botID, Data=命令）
	TypeCLIResult   = "cli_result"   // 主程序→CLI：执行结果（Data=输出(base64)、ExitCode、MsgID）
	TypeCLIError    = "cli_error"    // 主程序→CLI：请求级错误（Data=错误信息）
)

// HeartbeatInterval 用户端发送心跳的间隔
const HeartbeatInterval = 5 * time.Second

// HeartbeatPeriod 服务端心跳检测周期
const HeartbeatPeriod = 10 * time.Second

// MaxMissed 允许连续丢失心跳的周期数。
// 需求：1 个周期没收到 → 指针进缓存区列表继续等待；3 个周期没收到 → 销毁协程，机器下线。
const MaxMissed = 3

// DefaultExecTimeoutSec 一次性命令默认超时（秒）
const DefaultExecTimeoutSec = 120

// MaxExecTimeoutSec 一次性命令允许的最大超时（秒）
const MaxExecTimeoutSec = 600

// Message 两端通信的统一消息结构。
// 终端是原始字节流（含 ANSI 控制序列，且可能在 UTF-8 多字节字符中间分包），
// 所以 input/output 的 Data 一律使用 base64 承载，保证二进制安全、不被 JSON 损坏。
// exec 通道的命令/输出同样使用 base64，统一处理且避免编码问题。
type Message struct {
	Type     string `json:"type"`
	UserID   string `json:"userID,omitempty"`   // botID / userID / 目标 botID
	Data     string `json:"data,omitempty"`     // 终端字节流、命令文本或输出（均 base64）
	MsgID    string `json:"msgID,omitempty"`    // exec/cli 请求关联 ID
	ExitCode int    `json:"exitCode,omitempty"` // exec_result：命令退出码（-1=超时/启动失败）
	Timeout  int    `json:"timeout,omitempty"`  // exec/cli：超时秒数
	Cols     int    `json:"cols,omitempty"`     // resize：列数
	Rows     int    `json:"rows,omitempty"`     // resize：行数
	OS       string `json:"os,omitempty"`       // register：被控机系统信息（如 windows/amd64 (10.0.26200)）
	Name     string `json:"name,omitempty"`     // register：被控机自定义名称（用户在 config.json 里填写）
}

// EncodeB64 把原始字节编码进消息字段
func EncodeB64(p []byte) string { return base64.StdEncoding.EncodeToString(p) }

// DecodeB64 还原消息字段中的原始字节
func DecodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
