package core

import (
	"time"

	"remoteassist-helper/logx"
)

// 闲置下线确认的三个参数：
//   - IdleTimeout：连续多久没有操作 → 向占用者发出「是否下线」；
//   - IdleConfirm：询问后的倒计时长度，倒计时内没点「否」就自动下线；
//   - idleScanEvery：扫描周期，决定询问与超时的判定精度。
//
// 「有操作」= 用户有输入 或 机器有输出（见 BOT.Send / BOT.PushOut），
// 因此正在装环境、拉依赖这类长时间刷屏的任务不会被误判为闲置。
const (
	IdleTimeout   = 5 * time.Minute
	IdleConfirm   = 1 * time.Minute
	idleScanEvery = 15 * time.Second
)

// StartIdleWatch 后台协程：每个扫描周期检查一次所有在线机器。
//
// 每轮对每台机器：
//  1. 没有占用者（空闲）→ 跳过；
//  2. 未在询问且「距上次操作超过 IdleTimeout」→ 发出询问，置倒计时截止时间；
//  3. 已在询问且已过截止时间 → 收回控制权（让位给队首）；
//  4. 占用者点了「否」→ 由 AnswerIdle 重置计时，这里自然跳过。
func StartIdleWatch(m *Manager, prog *logx.ProgramLog) {
	go func() {
		ticker := time.NewTicker(idleScanEvery)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			for _, b := range m.Snapshot() {
				_, lastActive, asked, deadline := b.IdleState()
				if lastActive.IsZero() {
					continue // 无占用者
				}
				if !b.HolderIsBrowser() {
					continue // 本机 DOS 终端没有应答界面，不参与闲置询问
				}
				if !asked {
					if now.Sub(lastActive) >= IdleTimeout {
						if b.AskIdle(now.Add(IdleConfirm)) {
							prog.Info("bot %s 占用者超过 %v 无操作，已询问是否下线", b.ID, IdleTimeout)
						}
					}
					continue
				}
				if now.After(deadline) {
					prog.Info("bot %s 占用者未应答下线询问，收回控制权", b.ID)
					b.RevokeHolder("无操作超时")
				}
			}
		}
	}()
}
