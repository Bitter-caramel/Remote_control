package store

import (
	"database/sql"
	"errors"
	"time"
)

// Session 一次登录会话。JWT 里只放 sid，真正的有效期与撤销状态以本表为准。
type Session struct {
	ID           string
	UserID       int64
	TokenVersion int64
	CreatedAt    time.Time
	ExpiresAt    time.Time
	RevokedAt    time.Time
	IP           string
	UserAgent    string
}

// CreateSession 登记一次登录
func (s *Store) CreateSession(sess Session) error {
	_, err := s.db.Exec(`INSERT INTO sessions
		(id, user_id, token_version, created_at, expires_at, revoked_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		sess.ID, sess.UserID, sess.TokenVersion,
		toTS(sess.CreatedAt), toTS(sess.ExpiresAt), sess.IP, sess.UserAgent)
	return err
}

// SessionByID 查询会话；不存在返回 ErrNotFound
func (s *Store) SessionByID(sid string) (*Session, error) {
	var (
		sess Session
		ca   int64
		ea   int64
		ra   int64
	)
	err := s.db.QueryRow(`SELECT id, user_id, token_version, created_at, expires_at,
		revoked_at, ip, user_agent FROM sessions WHERE id = ?`, sid).
		Scan(&sess.ID, &sess.UserID, &sess.TokenVersion, &ca, &ea, &ra, &sess.IP, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt = fromTS(ca)
	sess.ExpiresAt = fromTS(ea)
	sess.RevokedAt = fromTS(ra)
	return &sess, nil
}

// Active 会话当前是否仍然有效（未撤销、未过期）
func (sess *Session) Active(now time.Time) bool {
	return sess.RevokedAt.IsZero() && now.Before(sess.ExpiresAt)
}

// RevokeSession 登出：写 revoked_at，令牌立即失效（不影响其他用户）
func (s *Store) RevokeSession(sid string) error {
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at = ?
		WHERE id = ? AND revoked_at = 0`, toTS(time.Now()), sid)
	return err
}

// RevokeUserSessions 撤销某账号的全部活跃会话（改密码/停用后兜底清理）
func (s *Store) RevokeUserSessions(userID int64) (int64, error) {
	res, err := s.db.Exec(`UPDATE sessions SET revoked_at = ?
		WHERE user_id = ? AND revoked_at = 0`, toTS(time.Now()), userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneSessions 清理已过期或撤销时间超过保留期的会话记录，返回删除条数。
// 只删历史记录，不影响正在进行中的会话。
func (s *Store) PruneSessions(now time.Time, keep time.Duration) (int64, error) {
	cutoff := toTS(now.Add(-keep))
	res, err := s.db.Exec(`DELETE FROM sessions
		WHERE expires_at < ? OR (revoked_at > 0 AND revoked_at < ?)`,
		toTS(now), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
