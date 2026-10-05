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

// drainCtrl 排空控制通道，返回收到的消息类型
func drainCtrl(sub *Subscription) []string {
	var out []string
	for {
		select {
		case cm := <-sub.Ctrl:
			out = append(out, cm.Type)
		case <-time.After(30 * time.Millisecond):
			return out
		}
	}
}

// readOut 在超时内读取一份输出
func readOut(sub *Subscription, d time.Duration) ([]byte, bool) {
	select {
	case p, ok := <-sub.Out:
		return p, ok
	case <-time.After(d):
		return nil, false
	}
}

// TestCtxIsolation 不同上下文的输出互不可见
func TestCtxIsolation(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	b.EnsureCtx("c_b", 2, "乙")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	c := b.SubscribeCtx("c_b", user(2, "乙"), SubOwner)

	if b.CtxCount() != 2 {
		t.Fatalf("应有 2 条上下文, got %d", b.CtxCount())
	}
	b.PushCtxOut("c_a", []byte("A-only"))
	if p, ok := readOut(a, time.Second); !ok || string(p) != "A-only" {
		t.Fatalf("甲 应收到 A-only, got %q ok=%v", p, ok)
	}
	if p, ok := readOut(c, 100*time.Millisecond); ok {
		t.Fatalf("乙 不应看到甲的上下文输出, got %q", p)
	}
}

// TestWatchSyncBySegment 观看者只在段结束时收到内容
func TestWatchSyncBySegment(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	w := b.SubscribeCtx("c_a", user(2, "乙"), SubWatch)

	b.PushCtxOut("c_a", []byte("part1"))
	if p, ok := readOut(a, time.Second); !ok || string(p) != "part1" {
		t.Fatalf("owner 应实时收到 part1, got %q ok=%v", p, ok)
	}
	if p, ok := readOut(w, 100*time.Millisecond); ok {
		t.Fatalf("watch 不应实时收到内容, got %q", p)
	}

	b.PushCtxOut("c_a", []byte("part2"))
	b.PushCtxSegEnd("c_a", 1)
	if p, ok := readOut(w, time.Second); !ok || string(p) != "part1part2" {
		t.Fatalf("watch 应在段结束时收到整段, got %q ok=%v", p, ok)
	}
}

// TestWatchReplayOnlyFinishedSegments 观看者接入时只回放已完成的段
func TestWatchReplayOnlyFinishedSegments(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	owner := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	b.PushCtxOut("c_a", []byte("done"))
	if p, ok := readOut(owner, time.Second); !ok || string(p) != "done" {
		t.Fatalf("owner 应实时收到 done, got %q ok=%v", p, ok)
	}
	b.PushCtxSegEnd("c_a", 1)
	b.PushCtxOut("c_a", []byte("inflight"))

	w := b.SubscribeCtx("c_a", user(2, "乙"), SubWatch)
	if p, ok := readOut(w, time.Second); !ok || string(p) != "done" {
		t.Fatalf("watch 回放应只有已完成段, got %q ok=%v", p, ok)
	}
	if p, ok := readOut(owner, time.Second); !ok || string(p) != "inflight" {
		t.Fatalf("owner 应实时收到当前段, got %q ok=%v", p, ok)
	}
}

// TestInputLock 输入锁的取得与移交
func TestInputLock(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	if !a.Input {
		t.Fatal("owner 接入时应持有输入锁")
	}
	c := b.SubscribeCtx("c_a", user(2, "乙"), SubOperate)
	if c.Input {
		t.Fatal("operate 接入时不应立刻持锁（owner 还在）")
	}

	// 乙接续：甲被收回并收到提示
	if !b.TakeInput("c_a", user(2, "乙")) {
		t.Fatal("TakeInput 应成功")
	}
	if got := drainCtrl(a); len(got) != 1 || got[0] != CtrlRevoked {
		t.Fatalf("甲 应收到 revoked, got %v", got)
	}
	if got := drainCtrl(c); len(got) != 1 || got[0] != CtrlGranted {
		t.Fatalf("乙 应收到 granted, got %v", got)
	}

	// 锁在乙手上：甲再取回
	if !b.TakeInput("c_a", user(1, "甲")) {
		t.Fatal("owner 应能取回输入锁")
	}
	if got := drainCtrl(c); len(got) != 1 || got[0] != CtrlRevoked {
		t.Fatalf("乙 应收到 revoked, got %v", got)
	}
}

// TestRevokeGrantee 收回被授权者的输入锁，锁回到 owner
func TestRevokeGrantee(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	c := b.SubscribeCtx("c_a", user(2, "乙"), SubOperate)
	b.TakeInput("c_a", user(2, "乙"))
	drainCtrl(a)
	drainCtrl(c)

	if !b.RevokeGrantee("c_a", 2) {
		t.Fatal("RevokeGrantee 应返回 true")
	}
	if got := drainCtrl(c); len(got) != 1 || got[0] != CtrlRevoked {
		t.Fatalf("乙 应收到 revoked, got %v", got)
	}
	if got := drainCtrl(a); len(got) != 1 || got[0] != CtrlGranted {
		t.Fatalf("甲 应收到 granted, got %v", got)
	}
	// 乙的锁已被收回，再收回一次无效
	if b.RevokeGrantee("c_a", 2) {
		t.Fatal("重复收回应返回 false")
	}
}

// TestNotifyCtxOpened 上下文就绪/失败通知
func TestNotifyCtxOpened(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	b.NotifyCtxOpened("c_a", "")
	if got := drainCtrl(a); len(got) != 1 || got[0] != CtrlOpened {
		t.Fatalf("应收到 opened, got %v", got)
	}
	b.NotifyCtxOpened("c_a", "上下文数量已达上限")
	if got := drainCtrl(a); len(got) != 1 || got[0] != CtrlFailed {
		t.Fatalf("应收到 failed, got %v", got)
	}
}

// TestDropCtx 上下文终止时关闭订阅通道
func TestDropCtx(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	b.EnsureCtx("c_a", 1, "甲")
	a := b.SubscribeCtx("c_a", user(1, "甲"), SubOwner)
	b.DropCtx("c_a", "shell 已退出")
	if _, ok := <-a.Out; ok {
		t.Fatal("上下文终止后输出通道应被关闭")
	}
	if b.CtxCount() != 0 {
		t.Fatalf("上下文应被移除, got %d", b.CtxCount())
	}
}

// TestLocalCtxInput 本机保留上下文接入即持有输入锁
func TestLocalCtxInput(t *testing.T) {
	b := tmpBOT(t)
	defer b.Log().Close()

	s := b.SubscribeCtx(LocalCtxID, Subscriber{Kind: SubLocal, Display: LocalOwnerName}, SubOwner)
	if !s.Input {
		t.Fatal("本机上下文接入时应持有输入锁")
	}
	views := b.CtxViews()
	if len(views) != 1 || views[0].ID != LocalCtxID || views[0].InputName != LocalOwnerName {
		t.Fatalf("CtxViews 摘要错误: %+v", views)
	}
}
