package core

import (
	"testing"
	"time"
)

func tmpBOT(t *testing.T) *BOT {
	t.Helper()
	b, err := NewBOT("Btest", "127.0.0.1", 1, nil, t.TempDir())
	if err != nil {
		t.Fatalf("NewBOT: %v", err)
	}
	b.SetEventLog(NewEventLog(10))
	return b
}

func user(uid int64, name string) Subscriber {
	return Subscriber{Kind: SubBrowser, UserID: uid, Username: name, Display: name}
}

// 排空控制通道，返回收到的消息类型
func drain(sub *Subscription) []string {
	var out []string
	for {
		select {
		case cm := <-sub.Ctrl:
			out = append(out, cm.Type)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func TestArbitration(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	a := b.SubscribeAs(user(1, "甲"))
	if a.Mode != ModeOperator {
		t.Fatalf("甲 应为 operator, got %v", a.Mode)
	}
	if got := b.Occupancy(); !got.Occupied || got.Text != "甲 占用" || got.Queue != 0 {
		t.Fatalf("占用快照错误: %+v", got)
	}

	c := b.SubscribeAs(user(2, "乙"))
	if c.Mode != ModeWaiting || c.Queue != 1 {
		t.Fatalf("乙 应为 waiting#1, got mode=%v queue=%d", c.Mode, c.Queue)
	}
	if got := drain(c); len(got) != 1 || got[0] != CtrlQueued {
		t.Fatalf("乙 应收到 queued, got %v", got)
	}
	if got := b.Occupancy(); got.Text != "甲 占用" || got.Queue != 1 {
		t.Fatalf("排队后快照错误: %+v", got)
	}

	o := b.SubscribeAs(Subscriber{Kind: SubBrowser, UserID: 9, Username: "观察", Display: "观察", ReadOnly: true})
	if o.Mode != ModeObserver {
		t.Fatalf("观察者应为 observer, got %v", o.Mode)
	}
	if got := b.Occupancy(); got.Text != "甲 占用" || got.Observers != 1 {
		t.Fatalf("观察者不应改变占用文本: %+v", got)
	}

	// 甲断开 → 乙自动获得控制权
	a.Close()
	if got := drain(c); len(got) != 1 || got[0] != CtrlGranted {
		t.Fatalf("乙 应收到 granted, got %v", got)
	}
	if got := b.Occupancy(); got.Text != "乙 占用" || got.Queue != 0 {
		t.Fatalf("让位后快照错误: %+v", got)
	}

	// 乙被收回（闲置超时）
	if !b.AskIdle(time.Now().Add(time.Minute)) {
		t.Fatal("AskIdle 应返回 true")
	}
	if got := drain(c); len(got) != 1 || got[0] != CtrlIdlePrompt {
		t.Fatalf("乙 应收到 idle_prompt, got %v", got)
	}
	if b.AskIdle(time.Now().Add(time.Minute)) {
		t.Fatal("重复询问应被拒绝")
	}
	// 点「否」→ 重置，再次可询问
	b.AnswerIdle(c.id, false)
	if !b.AskIdle(time.Now().Add(time.Minute)) {
		t.Fatal("点否后应能再次询问")
	}
	drain(c)

	b.RevokeHolder("闲置")
	if got := b.Occupancy(); got.Occupied || got.Text != "无人占用" {
		t.Fatalf("收回后应无人占用: %+v", got)
	}
	// 观察者还在，仍可看到输出
	b.PushOut([]byte("hi"))
	if got := b.Occupancy(); got.Observers != 1 {
		t.Fatalf("观察者应仍在: %+v", got)
	}
	o.Close()

	// 事件：占用甲 / 排队乙 / 释放甲 / 占用乙 / 释放乙
	kinds := []EventKind{}
	for _, ev := range b.events.Snapshot() {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 5 {
		t.Fatalf("应有 5 条事件, got %d: %v", len(kinds), kinds)
	}

	// 空闲时主动放弃排队
	e2 := b.SubscribeAs(user(3, "丙"))
	if _, ok := b.ReservationFor(user(3, "丙")); !ok {
		t.Fatal("丙 应有预定记录")
	}
	if !b.Release(user(3, "丙")) {
		t.Fatal("Release 应返回 true")
	}
	if _, ok := b.ReservationFor(user(3, "丙")); ok {
		t.Fatal("放弃后不应再有预定记录")
	}
	drain(e2)
	e2.Close()
}

func TestCloseSubsNoPromote(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()
	b.SubscribeAs(user(1, "甲"))
	c := b.SubscribeAs(user(2, "乙"))
	drain(c) // 先取走接入时的 queued
	b.CloseSubs("机器离线")
	if got := drain(c); len(got) != 0 {
		t.Fatalf("机器离线不应让位, got %v", got)
	}
	if got := b.Occupancy(); got.Occupied || got.Queue != 0 {
		t.Fatalf("下线后应清空: %+v", got)
	}
}
