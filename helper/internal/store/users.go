package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound 记录不存在
var ErrNotFound = errors.New("记录不存在")

// ErrUsernameTaken 用户名已被占用
var ErrUsernameTaken = errors.New("账号名已存在")

// User 控制台账号（不含口令哈希，可安全下发到前端）
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        int       `json:"role"`
	RoleName    string    `json:"role_name"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastLoginAt time.Time `json:"last_login_at"`

	// TokenVersion 令牌版本：改口令/改角色/停用账号时 +1，
	// 使该账号此前签发的所有令牌立即失效。不下发到前端。
	TokenVersion int64 `json:"-"`
}

// Account 账号 + 口令校验材料，只在服务端鉴权路径上流转
type Account struct {
	User
	PasswordHash string
	Salt         string
	Iterations   int
}

const userCols = `id, username, display_name, role, disabled, token_version,
	created_at, updated_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u   User
		dis int
		ca  int64
		ua  int64
		la  int64
	)
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &dis, &u.TokenVersion,
		&ca, &ua, &la)
	if err != nil {
		return nil, err
	}
	u.RoleName = RoleName(u.Role)
	u.Disabled = dis != 0
	u.CreatedAt = fromTS(ca)
	u.UpdatedAt = fromTS(ua)
	u.LastLoginAt = fromTS(la)
	return &u, nil
}

// NormalizeUsername 账号名归一化：去空白 + 转小写，避免 Admin / admin 重复注册
func NormalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// CountUsers 账号总数（0 表示是全新部署，需要创建初始管理员）
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins 管理员数量（用于阻止删除/降级最后一个管理员）
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`,
		RoleAdmin).Scan(&n)
	return n, err
}

// CreateUser 新建账号，返回新账号信息
func (s *Store) CreateUser(username, displayName, hash, salt string, iterations, role int) (*User, error) {
	name := NormalizeUsername(username)
	if name == "" {
		return nil, errors.New("账号名不能为空")
	}
	if !ValidRole(role) {
		return nil, fmt.Errorf("非法的角色档位: %d", role)
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = name
	}
	now := toTS(time.Now())
	res, err := s.db.Exec(`INSERT INTO users
		(username, display_name, password_hash, salt, iterations, role, disabled,
		 token_version, created_at, updated_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 1, ?, ?, 0)`,
		name, displayName, hash, salt, iterations, role, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.UserByID(id)
}

// UserByID 按 ID 查询账号
func (s *Store) UserByID(id int64) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// AccountByUsername 按账号名查询（含口令校验材料），登录用
func (s *Store) AccountByUsername(username string) (*Account, error) {
	name := NormalizeUsername(username)
	row := s.db.QueryRow(`SELECT `+userCols+`, password_hash, salt, iterations
		FROM users WHERE username = ?`, name)
	return scanAccount(row)
}

// AccountByID 按 ID 查询（含口令校验材料）
func (s *Store) AccountByID(id int64) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+userCols+`, password_hash, salt, iterations
		FROM users WHERE id = ?`, id)
	return scanAccount(row)
}

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var (
		a   Account
		dis int
		ca  int64
		ua  int64
		la  int64
	)
	err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Role, &dis, &a.TokenVersion,
		&ca, &ua, &la, &a.PasswordHash, &a.Salt, &a.Iterations)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.RoleName = RoleName(a.Role)
	a.Disabled = dis != 0
	a.CreatedAt = fromTS(ca)
	a.UpdatedAt = fromTS(ua)
	a.LastLoginAt = fromTS(la)
	return &a, nil
}

// ListUsers 全部账号（按角色降序、ID 升序）
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY role DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]User, 0, 8)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *u)
	}
	return list, rows.Err()
}

// SetUserPassword 重置口令（盐与迭代次数一并更新），并让该账号所有旧令牌立即失效
func (s *Store) SetUserPassword(id int64, hash, salt string, iterations int) error {
	return s.execAffecting(
		`UPDATE users SET password_hash = ?, salt = ?, iterations = ?,
			token_version = token_version + 1, updated_at = ? WHERE id = ?`,
		hash, salt, iterations, toTS(time.Now()), id)
}

// SetUserRole 调整角色，并使旧令牌立即失效（避免降权后仍持有管理员令牌）
func (s *Store) SetUserRole(id int64, role int) error {
	if !ValidRole(role) {
		return fmt.Errorf("非法的角色档位: %d", role)
	}
	return s.execAffecting(
		`UPDATE users SET role = ?, token_version = token_version + 1, updated_at = ?
		 WHERE id = ?`, role, toTS(time.Now()), id)
}

// SetUserDisabled 启用/停用账号，停用时旧令牌立即失效
func (s *Store) SetUserDisabled(id int64, disabled bool) error {
	return s.execAffecting(
		`UPDATE users SET disabled = ?, token_version = token_version + 1, updated_at = ?
		 WHERE id = ?`, boolToInt(disabled), toTS(time.Now()), id)
}

// SetUserDisplayName 修改显示名（不影响权限，不失效令牌）
func (s *Store) SetUserDisplayName(id int64, displayName string) error {
	return s.execAffecting(
		`UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(displayName), toTS(time.Now()), id)
}

// MarkUserLogin 记录最近登录时间
func (s *Store) MarkUserLogin(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`,
		toTS(time.Now()), id)
	return err
}

// DeleteUser 删除账号（其会话由外键级联删除）
func (s *Store) DeleteUser(id int64) error {
	return s.execAffecting(`DELETE FROM users WHERE id = ?`, id)
}

// execAffecting 执行写操作并要求命中至少一行，未命中即记录不存在
func (s *Store) execAffecting(query string, args ...any) error {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
