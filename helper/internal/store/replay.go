package store

import (
	"database/sql"
	"errors"
	"time"
)

// replay.go：命令历史回放的持久化与保留策略。
//
// 每条操作上下文在「段」结束时把该段的原始输出落库（见 context_segments），
// 重连（helper 重启 / bot 重启导致内存回放丢失）时再按 seq 顺序取出喂给终端，
// 让用户看到上次关掉终端前的画面。helper core 的内存回放仍是实时首选，
// 本表只是它的「冷启动底片」。
//
// 保留量由管理员在「命令历史回放」面板设定：按条数或按天数二选一。策略一改，
// 服务端立即清理多余数据（不可恢复），因此本表不会无限增长。

// 回放保留策略的两种口径（二选一）
const (
	ReplayByCount = "count" // 每条上下文最多保留 N 段
	ReplayByDays  = "days"  // 只保留最近 N 天内的段
)

// 默认策略与取值边界
const (
	DefaultReplayMaxCount = 200
	DefaultReplayMaxDays  = 7
	MaxReplayCount        = 10000
	MaxReplayDays         = 3650

	// SegmentLimit 单段落库上限：超长的命令输出只保留末尾，避免一条
	// dir /s 之类把库撑爆。uplink 在累积阶段也用这个上限防止内存膨胀。
	SegmentLimit = 128 * 1024
	// recentQueryLimit 单次回放查询最多取回的段数（配合 RecentSegments 的字节上限）
	recentQueryLimit = 500
)

// ReplaySettings 回放保留策略
type ReplaySettings struct {
	Mode     string `json:"mode"`      // count | days
	MaxCount int    `json:"max_count"` // mode=count 时生效
	MaxDays  int    `json:"max_days"`  // mode=days 时生效
}

// ReplayStats 回放数据现状（面板展示用）
type ReplayStats struct {
	Segments int   `json:"segments"` // 总段数
	Bytes    int64 `json:"bytes"`    // 总占用字节
	Contexts int   `json:"contexts"` // 涉及上下文数
}

// ReplaySettings 读取当前保留策略；库里还没有记录时返回默认值。
func (s *Store) ReplaySettings() (ReplaySettings, error) {
	def := ReplaySettings{
		Mode:     ReplayByCount,
		MaxCount: DefaultReplayMaxCount,
		MaxDays:  DefaultReplayMaxDays,
	}
	var out ReplaySettings
	err := s.db.QueryRow(`SELECT mode, max_count, max_days FROM replay_settings WHERE id = 1`).
		Scan(&out.Mode, &out.MaxCount, &out.MaxDays)
	if err == nil {
		return normalizeReplay(out), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return def, err
}

// SaveReplaySettings 保存策略并立即按新策略清理多余数据（不可恢复）。
func (s *Store) SaveReplaySettings(in ReplaySettings) (ReplaySettings, error) {
	clean, err := validateReplay(in)
	if err != nil {
		return ReplaySettings{}, err
	}
	_, err = s.db.Exec(`INSERT INTO replay_settings (id, mode, max_count, max_days, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			mode = excluded.mode, max_count = excluded.max_count,
			max_days = excluded.max_days, updated_at = excluded.updated_at`,
		clean.Mode, clean.MaxCount, clean.MaxDays, toTS(time.Now()))
	if err != nil {
		return ReplaySettings{}, err
	}
	if err := s.enforceReplayAll(clean); err != nil {
		return clean, err
	}
	return clean, nil
}

// AppendSegment 落一段输出：超长截尾后写入，并按当前策略裁掉多余的旧段。
// 空段不落库（没有输出就没有可回放的内容）。
func (s *Store) AppendSegment(ctxID string, seq int, cwd string, data []byte) error {
	if ctxID == "" || len(data) == 0 {
		return nil
	}
	if len(data) > SegmentLimit {
		data = data[len(data)-SegmentLimit:]
	}
	if _, err := s.db.Exec(`INSERT INTO context_segments (context_id, seq, cwd, data, at)
		VALUES (?, ?, ?, ?, ?)`, ctxID, seq, cwd, data, toTS(time.Now())); err != nil {
		return err
	}
	cur, err := s.ReplaySettings()
	if err != nil {
		return err
	}
	return s.enforceReplayCtx(ctxID, cur)
}

// RecentSegments 取该上下文最近的若干段（按 seq 升序返回，总量不超过 maxBytes）。
// 用于重连时把上次的终端画面喂回去。
func (s *Store) RecentSegments(ctxID string, maxBytes int) ([][]byte, error) {
	if ctxID == "" || maxBytes <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT data FROM context_segments
		WHERE context_id = ? ORDER BY seq DESC, id DESC LIMIT ?`, ctxID, recentQueryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 从最新往前攒，攒够字节数就停；最后反转成时间顺序。
	var rev [][]byte
	total := 0
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		rev = append(rev, b)
		total += len(b)
		if total >= maxBytes {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([][]byte, len(rev))
	for i, b := range rev {
		out[len(rev)-1-i] = b
	}
	// 只保留末尾 maxBytes：截断最早那段的前部
	if total > maxBytes {
		cut := total - maxBytes
		for i, b := range out {
			if cut <= 0 {
				break
			}
			if cut >= len(b) {
				cut -= len(b)
				out[i] = nil
				continue
			}
			out[i] = b[cut:]
			cut = 0
		}
	}
	return out, nil
}

// LastSeq 该上下文已落库的最大段序号。
// 被控端重连（bot 重启后 shell 重建）时会从 1 重新计数，服务端在建上下文时
// 把这个基数下发过去，保证段序号跨重启单调，重放顺序才正确。
func (s *Store) LastSeq(ctxID string) int {
	var seq int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM context_segments WHERE context_id = ?`,
		ctxID).Scan(&seq); err != nil {
		return 0
	}
	return seq
}

// ReplayStats 统计回放数据现状（段数 / 占用字节 / 上下文数）
func (s *Store) ReplayStats() (ReplayStats, error) {
	var st ReplayStats
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(data)), 0),
		COUNT(DISTINCT context_id) FROM context_segments`).
		Scan(&st.Segments, &st.Bytes, &st.Contexts)
	return st, err
}

// ---------- 内部工具 ----------

// normalizeReplay 兜底修正库里可能残留的越界值（例如手工改库）
func normalizeReplay(in ReplaySettings) ReplaySettings {
	if in.Mode != ReplayByDays {
		in.Mode = ReplayByCount
	}
	if in.MaxCount <= 0 || in.MaxCount > MaxReplayCount {
		in.MaxCount = DefaultReplayMaxCount
	}
	if in.MaxDays <= 0 || in.MaxDays > MaxReplayDays {
		in.MaxDays = DefaultReplayMaxDays
	}
	return in
}

// validateReplay 校验管理员提交的策略
func validateReplay(in ReplaySettings) (ReplaySettings, error) {
	if in.Mode != ReplayByCount && in.Mode != ReplayByDays {
		return ReplaySettings{}, errors.New("保留方式只能是按条数或按天数")
	}
	if in.Mode == ReplayByCount {
		if in.MaxCount < 1 || in.MaxCount > MaxReplayCount {
			return ReplaySettings{}, errors.New("保留条数需在 1 ~ 10000 之间")
		}
	} else {
		if in.MaxDays < 1 || in.MaxDays > MaxReplayDays {
			return ReplaySettings{}, errors.New("保留天数需在 1 ~ 3650 之间")
		}
	}
	return in, nil
}

// enforceReplayAll 按策略清理全库（管理员保存设置后立即调用）
func (s *Store) enforceReplayAll(cfg ReplaySettings) error {
	if cfg.Mode == ReplayByDays {
		cut := toTS(time.Now().AddDate(0, 0, -cfg.MaxDays))
		_, err := s.db.Exec(`DELETE FROM context_segments WHERE at < ?`, cut)
		return err
	}
	rows, err := s.db.Query(`SELECT DISTINCT context_id FROM context_segments`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := s.enforceReplayCtx(id, cfg); err != nil {
			return err
		}
	}
	return nil
}

// enforceReplayCtx 按策略清理单条上下文的旧段（没有可删的行属正常，不算错误）
func (s *Store) enforceReplayCtx(ctxID string, cfg ReplaySettings) error {
	if cfg.Mode == ReplayByDays {
		cut := toTS(time.Now().AddDate(0, 0, -cfg.MaxDays))
		_, err := s.db.Exec(`DELETE FROM context_segments WHERE context_id = ? AND at < ?`,
			ctxID, cut)
		return err
	}
	// count：保留最新的 max_count 段（seq 可能因 bot 重启回到过小值，故并列 id）
	_, err := s.db.Exec(`DELETE FROM context_segments WHERE id IN (
			SELECT id FROM context_segments WHERE context_id = ?
			ORDER BY seq DESC, id DESC LIMIT -1 OFFSET ?)`,
		ctxID, cfg.MaxCount)
	return err
}
