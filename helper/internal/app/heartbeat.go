package app

import (
	"time"

	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

// StartHeartbeatChecker 后台协程：每个检测周期扫描一次所有在线 bot。
// 需求逻辑：
//   - 一个周期内没收到心跳 → 该 bot 的指针写入缓存区列表，继续等待；
//   - 三个周期没收到 → 销毁协程，bots 列表删除指针，缓存区列表也删除指针，机器下线。
func StartHeartbeatChecker(m *Manager, prog *logx.ProgramLog) {
	go func() {
		ticker := time.NewTicker(protocol.HeartbeatPeriod)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			for _, b := range m.Snapshot() {
				// 上个周期内收到过心跳 → 恢复；否则记一次丢失
				if now.UnixNano()-b.LastBeat.Load() < int64(protocol.HeartbeatPeriod) {
					m.Recover(b)
				} else {
					m.Miss(b)
				}
			}
		}
	}()
}
