package store

import "time"

// 审计动作常量
const (
	ActionLogin        = "login"         // 登录
	ActionLoginFailed  = "login_failed"  // 登录失败（含未知账号）
	ActionLogout       = "logout"        // 登出
	ActionUserCreate   = "user_create"   // 新建账号
	ActionUserRole     = "user_role"     // 调整角色
	ActionUserPassword = "user_password" // 重置口令
	ActionUserDisabled = "user_disabled" // 启用/停用
	ActionUserDelete   = "user_delete"   // 删除账号
	ActionBotMinRole   = "bot_min_role"  // 调整机器可见等级
	ActionBotNote      = "bot_note"      // 修改机器备注
	ActionCtxOpen      = "ctx_open"      // 打开/创建操作上下文
	ActionCtxRequest   = "ctx_request"   // 发起观看/接续申请
	ActionCtxAnswer    = "ctx_answer"    // 同意/拒绝申请
	ActionCtxGrant     = "ctx_grant"     // 主动授权（未经申请）
	ActionCtxRevoke    = "ctx_revoke"    // 收回授权
	ActionWatchDefault = "watch_default" // 设置用户级「默认可看」
	ActionReplayPolicy = "replay_policy" // 调整命令历史回放保留策略
)

// AuditEntry 一条审计记录
type AuditEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	UserID   int64     `json:"user_id"`
	Username string    `json:"username"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Detail   string    `json:"detail"`
	IP       string    `json:"ip"`
}

// Audit 写入一条审计记录。
// 审计不应影响主流程，调用方通常忽略其错误。
func (s *Store) Audit(e AuditEntry) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO audit_log
		(at, user_id, username, action, target, detail, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		toTS(at), e.UserID, e.Username, e.Action, e.Target, e.Detail, e.IP)
	return err
}

// ListAudit 最近 limit 条审计记录（新的在前）
func (s *Store) ListAudit(limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, at, user_id, username, action, target, detail, ip
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var (
			e  AuditEntry
			at int64
		)
		if err := rows.Scan(&e.ID, &at, &e.UserID, &e.Username, &e.Action,
			&e.Target, &e.Detail, &e.IP); err != nil {
			return nil, err
		}
		e.At = fromTS(at)
		list = append(list, e)
	}
	return list, rows.Err()
}
