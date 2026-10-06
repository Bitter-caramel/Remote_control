package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// context.go：操作上下文的持久化。
//
// 「用户ID → botID → 操作上下文」这棵关系树里，bot 只是分组维度，
// 真正的实体是 1:1 绑定 (bot, owner) 的上下文：每条上下文在被控端对应一个
// 独立 shell 进程，默认私密，跨用户观看或接续需要 owner 授权。
//
// 本文件只负责「数据 + 权限判定」，不碰内存路由：core 层拿到
// AccessModeFor 的结论后决定把输出推给谁、把输入锁给谁。

// 访问模式。owner > operate > watch > none。
const (
	ModeOwner   = "owner"
	ModeOperate = "operate"
	ModeWatch   = "watch"
	ModeNone    = "none"
)

// ValidGrantMode 是否为可授权的模式（owner 是身份而非授权，不能作为申请目标）
func ValidGrantMode(mode string) bool { return mode == ModeWatch || mode == ModeOperate }

// CtxID 由 (bot, owner) 唯一确定的上下文 ID。
// 用确定性 ID 而不是自增/随机：CreateOrGetContext 因此天然幂等，
// 且 DB、内存态、协议里的 ctxID 三处永远一致。
func CtxID(botID string, ownerID int64) string {
	return fmt.Sprintf("c_%s_%d", botID, ownerID)
}

// Context 一条操作上下文（含 owner 显示名，便于前端直接展示）
type Context struct {
	ID           string    `json:"id"`
	BotID        string    `json:"bot_id"`
	OwnerID      int64     `json:"owner_id"`
	OwnerName    string    `json:"owner_name"`
	Title        string    `json:"title"`
	Cwd          string    `json:"cwd"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// ContextGrant 一条已生效的授权
type ContextGrant struct {
	ID        int64     `json:"id"`
	ContextID string    `json:"context_id"`
	BotID     string    `json:"bot_id"`
	Title     string    `json:"title"`
	GranteeID int64     `json:"grantee_id"`
	Grantee   string    `json:"grantee"`
	Mode      string    `json:"mode"`
	GrantedAt time.Time `json:"granted_at"`
}

// ContextRequest 一条申请记录
type ContextRequest struct {
	ID          int64     `json:"id"`
	ContextID   string    `json:"context_id"`
	BotID       string    `json:"bot_id"`
	OwnerID     int64     `json:"owner_id"`
	RequesterID int64     `json:"requester_id"`
	Requester   string    `json:"requester"`
	Mode        string    `json:"mode"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	AnsweredAt  time.Time `json:"answered_at"`
}

const contextCols = `SELECT c.id, c.bot_id, c.owner_id, COALESCE(u.display_name, ''),
		c.title, c.cwd, c.created_at, c.updated_at, c.last_active_at
	FROM contexts c LEFT JOIN users u ON u.id = c.owner_id`

func scanContext(row interface{ Scan(...any) error }) (*Context, error) {
	var (
		c          Context
		ca, ua, la int64
	)
	err := row.Scan(&c.ID, &c.BotID, &c.OwnerID, &c.OwnerName, &c.Title, &c.Cwd, &ca, &ua, &la)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt, c.LastActiveAt = fromTS(ca), fromTS(ua), fromTS(la)
	return &c, nil
}

// CreateOrGetContext 取得我的上下文，不存在则创建（幂等，同时刷新活跃时间）
func (s *Store) CreateOrGetContext(botID string, ownerID int64) (*Context, error) {
	if botID == "" || ownerID <= 0 {
		return nil, errors.New("无效的上下文参数")
	}
	id := CtxID(botID, ownerID)
	now := toTS(time.Now())
	_, err := s.db.Exec(`INSERT INTO contexts
		(id, bot_id, owner_id, title, cwd, created_at, updated_at, last_active_at)
		VALUES (?, ?, ?, '', '', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_active_at = excluded.last_active_at`,
		id, botID, ownerID, now, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetContext(id)
}

// GetContext 按 ID 查询上下文
func (s *Store) GetContext(id string) (*Context, error) {
	return scanContext(s.db.QueryRow(contextCols+` WHERE c.id = ?`, id))
}

// ListContextsForBot 某台 bot 上的全部上下文（不论归属）
func (s *Store) ListContextsForBot(botID string) ([]Context, error) {
	return s.queryContexts(contextCols+` WHERE c.bot_id = ? ORDER BY c.created_at ASC`, botID)
}

// ListMyContexts 我拥有的全部上下文（跨 bot）
func (s *Store) ListMyContexts(ownerID int64) ([]Context, error) {
	return s.queryContexts(contextCols+` WHERE c.owner_id = ? ORDER BY c.last_active_at DESC`, ownerID)
}

// AccessibleContexts 我在某台 bot 上能看到的上下文：
// 我自己的 + 别人授权给我的 + 别人开了「默认可看」的。
func (s *Store) AccessibleContexts(botID string, userID int64, role int) ([]Context, error) {
	all, err := s.ListContextsForBot(botID)
	if err != nil {
		return nil, err
	}
	out := make([]Context, 0, len(all))
	for _, c := range all {
		if s.AccessModeFor(c.ID, userID, role) != ModeNone {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) queryContexts(query string, args ...any) ([]Context, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Context, 0, 4)
	for rows.Next() {
		c, err := scanContext(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	return list, rows.Err()
}

// UpdateCwd 记录上下文当前的工作目录（每段结束时刷新，重连后据此恢复现场）
func (s *Store) UpdateCwd(ctxID, cwd string) error {
	if ctxID == "" {
		return errors.New("上下文 ID 不能为空")
	}
	now := toTS(time.Now())
	return s.execAffecting(
		`UPDATE contexts SET cwd = ?, updated_at = ?, last_active_at = ? WHERE id = ?`,
		cwd, now, now, ctxID)
}

// CreateRequest 发起观看/接续申请；同一上下文同一申请人已有待处理申请时直接复用，
// 避免用户连点造成一堆重复申请。
func (s *Store) CreateRequest(ctxID string, requesterID int64, mode string) (*ContextRequest, error) {
	if !ValidGrantMode(mode) {
		return nil, errors.New("非法的申请模式")
	}
	c, err := s.GetContext(ctxID)
	if err != nil {
		return nil, err
	}
	if c.OwnerID == requesterID {
		return nil, errors.New("不能申请自己的上下文")
	}

	// 同一 (上下文, 申请人) 只保留一条待处理申请：已存在则把模式更新为本次申请的模式，
	// 这样「先申请观看、再申请接续」能正确升级，而不是被旧申请静默顶替。
	var existing int64
	err = s.db.QueryRow(`SELECT id FROM context_requests
		WHERE context_id = ? AND requester_id = ? AND state = 'pending'`,
		ctxID, requesterID).Scan(&existing)
	if err == nil {
		if _, err := s.db.Exec(`UPDATE context_requests SET mode = ? WHERE id = ?`,
			mode, existing); err != nil {
			return nil, err
		}
		return s.RequestByID(existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	res, err := s.db.Exec(`INSERT INTO context_requests
		(context_id, requester_id, mode, state, created_at, answered_at)
		VALUES (?, ?, ?, 'pending', ?, 0)`, ctxID, requesterID, mode, toTS(time.Now()))
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.RequestByID(id)
}

// RequestByID 按 ID 查询申请
func (s *Store) RequestByID(id int64) (*ContextRequest, error) {
	row := s.db.QueryRow(`SELECT r.id, r.context_id, c.bot_id, c.owner_id, r.requester_id,
			COALESCE(u.display_name, ''), r.mode, r.state, r.created_at, r.answered_at
		FROM context_requests r
		JOIN contexts c ON c.id = r.context_id
		LEFT JOIN users u ON u.id = r.requester_id
		WHERE r.id = ?`, id)
	var (
		r      ContextRequest
		ca, aa int64
	)
	err := row.Scan(&r.ID, &r.ContextID, &r.BotID, &r.OwnerID, &r.RequesterID,
		&r.Requester, &r.Mode, &r.State, &ca, &aa)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.AnsweredAt = fromTS(ca), fromTS(aa)
	return &r, nil
}

// ListPendingRequests 我作为 owner 的待处理申请（最早的在前）
func (s *Store) ListPendingRequests(ownerID int64) ([]ContextRequest, error) {
	rows, err := s.db.Query(`SELECT r.id, r.context_id, c.bot_id, c.owner_id, r.requester_id,
			COALESCE(u.display_name, ''), r.mode, r.state, r.created_at, r.answered_at
		FROM context_requests r
		JOIN contexts c ON c.id = r.context_id
		LEFT JOIN users u ON u.id = r.requester_id
		WHERE c.owner_id = ? AND r.state = 'pending'
		ORDER BY r.created_at ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]ContextRequest, 0, 4)
	for rows.Next() {
		var (
			r      ContextRequest
			ca, aa int64
		)
		if err := rows.Scan(&r.ID, &r.ContextID, &r.BotID, &r.OwnerID, &r.RequesterID,
			&r.Requester, &r.Mode, &r.State, &ca, &aa); err != nil {
			return nil, err
		}
		r.CreatedAt, r.AnsweredAt = fromTS(ca), fromTS(aa)
		list = append(list, r)
	}
	return list, rows.Err()
}

// AnswerRequest 同意/拒绝一条申请。同意时同时建立（或更新）授权记录。
// 返回被处理的申请，调用方据其中的 ContextID / RequesterID / Mode 同步内存路由与输入锁。
func (s *Store) AnswerRequest(reqID int64, accept bool) (*ContextRequest, error) {
	req, err := s.RequestByID(reqID)
	if err != nil {
		return nil, err
	}
	if req.State != "pending" {
		return nil, errors.New("该申请已处理")
	}

	state := "rejected"
	if accept {
		state = "accepted"
	}
	now := toTS(time.Now())

	// 单连接 + 事务：授权与申请状态必须一起生效，否则会出现
	// 「申请已同意但授权未建立」或反之的悬空状态。
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE context_requests SET state = ?, answered_at = ? WHERE id = ?`,
		state, now, reqID); err != nil {
		return nil, err
	}
	if accept {
		if _, err := tx.Exec(`INSERT INTO context_grants
			(context_id, grantee_id, mode, granted_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(context_id, grantee_id) DO UPDATE SET
				mode = excluded.mode, granted_at = excluded.granted_at`,
			req.ContextID, req.RequesterID, req.Mode, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	req.State = state
	req.AnsweredAt = fromTS(now)
	return req, nil
}

// GrantAccess 直接建立授权（owner 主动开放，无需申请）
func (s *Store) GrantAccess(ctxID string, granteeID int64, mode string) error {
	if !ValidGrantMode(mode) {
		return errors.New("非法的授权模式")
	}
	_, err := s.db.Exec(`INSERT INTO context_grants
		(context_id, grantee_id, mode, granted_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(context_id, grantee_id) DO UPDATE SET
			mode = excluded.mode, granted_at = excluded.granted_at`,
		ctxID, granteeID, mode, toTS(time.Now()))
	return err
}

// ListGrants 我已发出的全部授权（我拥有的上下文上的授权）
func (s *Store) ListGrants(ownerID int64) ([]ContextGrant, error) {
	rows, err := s.db.Query(`SELECT g.id, g.context_id, c.bot_id, c.title, g.grantee_id,
			COALESCE(u.display_name, ''), g.mode, g.granted_at
		FROM context_grants g
		JOIN contexts c ON c.id = g.context_id
		LEFT JOIN users u ON u.id = g.grantee_id
		WHERE c.owner_id = ?
		ORDER BY g.granted_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]ContextGrant, 0, 4)
	for rows.Next() {
		var (
			g  ContextGrant
			at int64
		)
		if err := rows.Scan(&g.ID, &g.ContextID, &g.BotID, &g.Title, &g.GranteeID,
			&g.Grantee, &g.Mode, &at); err != nil {
			return nil, err
		}
		g.GrantedAt = fromTS(at)
		list = append(list, g)
	}
	return list, rows.Err()
}

// GrantByID 按授权 ID 查询（收回时用，需据此拿到上下文与被授权人）
func (s *Store) GrantByID(id int64) (*ContextGrant, error) {
	row := s.db.QueryRow(`SELECT g.id, g.context_id, c.bot_id, c.title, g.grantee_id,
			COALESCE(u.display_name, ''), g.mode, g.granted_at
		FROM context_grants g
		JOIN contexts c ON c.id = g.context_id
		LEFT JOIN users u ON u.id = g.grantee_id
		WHERE g.id = ?`, id)
	var (
		g  ContextGrant
		at int64
	)
	err := row.Scan(&g.ID, &g.ContextID, &g.BotID, &g.Title, &g.GranteeID,
		&g.Grantee, &g.Mode, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	g.GrantedAt = fromTS(at)
	return &g, nil
}

// GrantMode 某人对我某条上下文的授权模式；无授权返回空串
func (s *Store) GrantMode(ctxID string, granteeID int64) string {
	var mode string
	err := s.db.QueryRow(`SELECT mode FROM context_grants
		WHERE context_id = ? AND grantee_id = ?`, ctxID, granteeID).Scan(&mode)
	if err != nil {
		return ""
	}
	return mode
}

// RevokeGrant 收回授权；调用方需同时断开该用户的订阅并把输入锁收回 owner
func (s *Store) RevokeGrant(ctxID string, granteeID int64) error {
	return s.execAffecting(`DELETE FROM context_grants
		WHERE context_id = ? AND grantee_id = ?`, ctxID, granteeID)
}

// SetWatchDefault 设置用户级「默认可看」开关
func (s *Store) SetWatchDefault(userID int64, on bool) error {
	_, err := s.db.Exec(`INSERT INTO user_prefs (user_id, watch_default) VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET watch_default = excluded.watch_default`,
		userID, boolToInt(on))
	return err
}

// WatchDefault 用户级「默认可看」是否开启
func (s *Store) WatchDefault(userID int64) bool {
	var v int
	if err := s.db.QueryRow(
		`SELECT watch_default FROM user_prefs WHERE user_id = ?`, userID).Scan(&v); err != nil {
		return false
	}
	return v != 0
}

// AccessModeFor 核心权限判定：返回 owner | operate | watch | none。
// 判定顺序：本人 > 管理员 > 显式授权 > owner 的「默认可看」。
// 单个上下文要单独关掉时，先撤销该 grant 即可（watch_default 是兜底而非覆盖）。
func (s *Store) AccessModeFor(ctxID string, userID int64, role int) string {
	c, err := s.GetContext(ctxID)
	if err != nil {
		return ModeNone
	}
	if c.OwnerID == userID {
		return ModeOwner
	}
	if role >= RoleAdmin {
		// 管理员拥有上帝权限，按 owner 级处理（可实时看、可拿输入锁）
		return ModeOwner
	}
	if m := s.GrantMode(ctxID, userID); m != "" {
		return m
	}
	if s.WatchDefault(c.OwnerID) {
		return ModeWatch
	}
	return ModeNone
}
