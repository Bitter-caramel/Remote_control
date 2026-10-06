package store

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// 回放相关 SQL 无法靠编译发现错误，这里覆盖：落段、按序取回、按条数/按天数裁剪。
func TestReplaySegmentsAndRetention(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	u, err := s.CreateUser("alice", "爱丽丝", "h", "s", 1, RoleOperator)
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	ctx, err := s.CreateOrGetContext("bot-1", u.ID)
	if err != nil {
		t.Fatalf("建上下文失败: %v", err)
	}

	// 默认策略：按条数、200 段
	cfg, err := s.ReplaySettings()
	if err != nil || cfg.Mode != ReplayByCount || cfg.MaxCount != DefaultReplayMaxCount {
		t.Fatalf("默认策略 = %+v（err=%v）", cfg, err)
	}

	// 空段不落库
	if err := s.AppendSegment(ctx.ID, 1, "", nil); err != nil {
		t.Fatalf("落空段失败: %v", err)
	}
	if got := s.LastSeq(ctx.ID); got != 0 {
		t.Fatalf("空段不应入库，LastSeq = %d", got)
	}

	for i, text := range []string{"one", "two", "three", "four"} {
		if err := s.AppendSegment(ctx.ID, i+1, `C:\work`, []byte(text)); err != nil {
			t.Fatalf("落段失败: %v", err)
		}
	}
	segs, err := s.RecentSegments(ctx.ID, 4096)
	if err != nil {
		t.Fatalf("取回放失败: %v", err)
	}
	if len(segs) != 4 || string(segs[0]) != "one" || string(segs[3]) != "four" {
		t.Fatalf("回放顺序不对: %q", segs)
	}
	// 字节上限：只保留末尾 7 字节 → "two"+"three"+"four" 的尾部
	tail, err := s.RecentSegments(ctx.ID, 7)
	if err != nil {
		t.Fatalf("取回放失败: %v", err)
	}
	joined := bytes.Join(tail, nil)
	if string(joined) != "reefour" {
		t.Fatalf("字节上限未生效: %q，期望 reefour", joined)
	}

	// 按条数裁剪：只留最近 2 段
	if _, err := s.SaveReplaySettings(ReplaySettings{Mode: ReplayByCount, MaxCount: 2}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	segs, _ = s.RecentSegments(ctx.ID, 4096)
	if len(segs) != 2 || string(segs[0]) != "three" || string(segs[1]) != "four" {
		t.Fatalf("按条数裁剪后 = %q，期望 [three four]", segs)
	}

	// 按天数裁剪：把现有段改成 3 天前，再关到 1 天并落一段触发清理
	if _, err := s.db.Exec(`UPDATE context_segments SET at = ?`, toTS(time.Now().AddDate(0, 0, -3))); err != nil {
		t.Fatalf("改时间失败: %v", err)
	}
	if _, err := s.SaveReplaySettings(ReplaySettings{Mode: ReplayByDays, MaxDays: 1}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := s.AppendSegment(ctx.ID, 5, "", []byte("fresh")); err != nil {
		t.Fatalf("落段失败: %v", err)
	}
	segs, _ = s.RecentSegments(ctx.ID, 4096)
	if len(segs) != 1 || string(segs[0]) != "fresh" {
		t.Fatalf("按天数裁剪后 = %q，期望 [fresh]", segs)
	}

	// 统计
	st, err := s.ReplayStats()
	if err != nil || st.Segments != 1 || st.Contexts != 1 || st.Bytes != int64(len("fresh")) {
		t.Fatalf("统计 = %+v（err=%v）", st, err)
	}

	// 非法策略被拒
	if _, err := s.SaveReplaySettings(ReplaySettings{Mode: "minute", MaxCount: 1}); err == nil {
		t.Fatal("非法保留方式应被拒绝")
	}
}
