package store

import (
	"path/filepath"
	"testing"
)

// 上下文相关的 SQL 与权限判定无法靠编译发现错误，这里用临时库跑一遍完整链路：
// 建表 → 建上下文 → 申请 → 同意 → 授权生效 → 撤销 → 默认可看兜底。
func TestContextAccessFlow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	a, err := s.CreateUser("alice", "爱丽丝", "h", "s", 1, RoleOperator)
	if err != nil {
		t.Fatalf("建用户 A 失败: %v", err)
	}
	b, err := s.CreateUser("bob", "鲍勃", "h", "s", 1, RoleOperator)
	if err != nil {
		t.Fatalf("建用户 B 失败: %v", err)
	}

	ctx, err := s.CreateOrGetContext("bot-1", a.ID)
	if err != nil {
		t.Fatalf("建上下文失败: %v", err)
	}
	if ctx.ID != CtxID("bot-1", a.ID) {
		t.Fatalf("上下文 ID 不符合约定: %s", ctx.ID)
	}
	// 幂等：重复创建返回同一条
	again, err := s.CreateOrGetContext("bot-1", a.ID)
	if err != nil || again.ID != ctx.ID {
		t.Fatalf("CreateOrGetContext 不幂等: %v %+v", err, again)
	}

	// 默认私密
	if got := s.AccessModeFor(ctx.ID, a.ID, a.Role); got != ModeOwner {
		t.Fatalf("owner 视角 = %s，期望 owner", got)
	}
	if got := s.AccessModeFor(ctx.ID, b.ID, b.Role); got != ModeNone {
		t.Fatalf("他人视角 = %s，期望 none（默认私密）", got)
	}

	// 申请 → owner 看到待处理 → 同意 → 授权生效
	req, err := s.CreateRequest(ctx.ID, b.ID, ModeWatch)
	if err != nil {
		t.Fatalf("发起申请失败: %v", err)
	}
	// 重复申请复用同一条
	dup, err := s.CreateRequest(ctx.ID, b.ID, ModeWatch)
	if err != nil || dup.ID != req.ID {
		t.Fatalf("重复申请未复用: %v %+v", err, dup)
	}
	// 已有待处理申请时改申请模式：仍复用同一条，且模式被更新为本次申请的模式
	up, err := s.CreateRequest(ctx.ID, b.ID, ModeOperate)
	if err != nil || up.ID != req.ID || up.Mode != ModeOperate {
		t.Fatalf("申请模式升级失败: %v %+v", err, up)
	}
	// 复位为 watch，后续按原用例继续
	if _, err := s.CreateRequest(ctx.ID, b.ID, ModeWatch); err != nil {
		t.Fatalf("复位申请模式失败: %v", err)
	}
	pending, err := s.ListPendingRequests(a.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("待处理申请 = %d（err=%v），期望 1", len(pending), err)
	}
	if _, err := s.AnswerRequest(req.ID, true); err != nil {
		t.Fatalf("同意申请失败: %v", err)
	}
	if got := s.AccessModeFor(ctx.ID, b.ID, b.Role); got != ModeWatch {
		t.Fatalf("同意后 = %s，期望 watch", got)
	}
	// 已处理的申请不能重复处理
	if _, err := s.AnswerRequest(req.ID, true); err == nil {
		t.Fatal("重复处理申请应当报错")
	}

	// 收回授权
	if err := s.RevokeGrant(ctx.ID, b.ID); err != nil {
		t.Fatalf("收回授权失败: %v", err)
	}
	if got := s.AccessModeFor(ctx.ID, b.ID, b.Role); got != ModeNone {
		t.Fatalf("收回后 = %s，期望 none", got)
	}

	// 用户级「默认可看」兜底为只读
	if err := s.SetWatchDefault(a.ID, true); err != nil {
		t.Fatalf("设置默认可看失败: %v", err)
	}
	if got := s.AccessModeFor(ctx.ID, b.ID, b.Role); got != ModeWatch {
		t.Fatalf("默认可看开启后 = %s，期望 watch", got)
	}

	// 列我可访问的上下文（A 自己 + B 通过默认可看）
	if list, err := s.AccessibleContexts("bot-1", b.ID, b.Role); err != nil || len(list) != 1 {
		t.Fatalf("B 可访问上下文 = %d（err=%v），期望 1", len(list), err)
	}
	if g, err := s.ListGrants(a.ID); err != nil || len(g) != 0 {
		t.Fatalf("授权列表 = %d（err=%v），期望 0（已收回）", len(g), err)
	}

	// cwd 与段记录
	if err := s.UpdateCwd(ctx.ID, `C:\work`); err != nil {
		t.Fatalf("更新 cwd 失败: %v", err)
	}
	if err := s.AppendSegment(ctx.ID, 1, `C:\work`, []byte("dir\r\n")); err != nil {
		t.Fatalf("记录段失败: %v", err)
	}
	if err := s.AppendSegment(ctx.ID, 2, `C:\work\sub`, []byte("cd sub\r\n")); err != nil {
		t.Fatalf("记录段失败: %v", err)
	}
	if got := s.LastSeq(ctx.ID); got != 2 {
		t.Fatalf("LastSeq = %d，期望 2", got)
	}
	if c, err := s.GetContext(ctx.ID); err != nil || c.Cwd != `C:\work` {
		t.Fatalf("cwd 未持久化: %v %+v", err, c)
	}
}
