package store

import (
	"database/sql"
	"errors"
	"time"
)

// Bot 一台连接过的被控机的持久信息。
// MinRole 决定哪些角色能看到并操作它：能看 = 自身角色 >= MinRole；
// 能操作 = 自身角色 >= 2 且 >= MinRole。
type Bot struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OS        string    `json:"os"`
	MinRole   int       `json:"min_role"`
	Note      string    `json:"note"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// UpsertBot 机器上线时惰性登记：新机器按默认等级写入，
// 老机器只刷新名称/系统/最近在线时间，绝不覆盖管理员设定的 MinRole 与备注。
func (s *Store) UpsertBot(id, name, osName string) error {
	now := toTS(time.Now())
	_, err := s.db.Exec(`INSERT INTO bots
		(id, name, os, min_role, note, first_seen, last_seen)
		VALUES (?, ?, ?, ?, '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			os = excluded.os,
			last_seen = excluded.last_seen`,
		id, name, osName, DefaultBotMinRole, now, now)
	return err
}

// BotByID 查询机器；不存在返回 ErrNotFound
func (s *Store) BotByID(id string) (*Bot, error) {
	var (
		b  Bot
		fs int64
		ls int64
	)
	err := s.db.QueryRow(`SELECT id, name, os, min_role, note, first_seen, last_seen
		FROM bots WHERE id = ?`, id).Scan(&b.ID, &b.Name, &b.OS, &b.MinRole, &b.Note, &fs, &ls)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.FirstSeen = fromTS(fs)
	b.LastSeen = fromTS(ls)
	return &b, nil
}

// MinRoleOf 返回机器的最低可见角色；未登记过的机器按默认值处理
func (s *Store) MinRoleOf(id string) int {
	b, err := s.BotByID(id)
	if err != nil {
		return DefaultBotMinRole
	}
	return b.MinRole
}

// ListBots 全部登记过的机器（最近在线在前）
func (s *Store) ListBots() ([]Bot, error) {
	rows, err := s.db.Query(`SELECT id, name, os, min_role, note, first_seen, last_seen
		FROM bots ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]Bot, 0, 8)
	for rows.Next() {
		var (
			b  Bot
			fs int64
			ls int64
		)
		if err := rows.Scan(&b.ID, &b.Name, &b.OS, &b.MinRole, &b.Note, &fs, &ls); err != nil {
			return nil, err
		}
		b.FirstSeen = fromTS(fs)
		b.LastSeen = fromTS(ls)
		list = append(list, b)
	}
	return list, rows.Err()
}

// MinRoles 一次性取出所有机器的最低可见角色（过滤机器列表时避免逐台查询）
func (s *Store) MinRoles() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT id, min_role FROM bots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]int, 8)
	for rows.Next() {
		var (
			id  string
			min int
		)
		if err := rows.Scan(&id, &min); err != nil {
			return nil, err
		}
		m[id] = min
	}
	return m, rows.Err()
}

// SetBotMinRole 设定机器的最低可见角色
func (s *Store) SetBotMinRole(id string, minRole int) error {
	if !ValidRole(minRole) {
		return errors.New("非法的机器等级")
	}
	return s.execAffecting(`UPDATE bots SET min_role = ? WHERE id = ?`, minRole, id)
}

// SetBotNote 设定机器备注
func (s *Store) SetBotNote(id, note string) error {
	return s.execAffecting(`UPDATE bots SET note = ? WHERE id = ?`, note, id)
}

// DeleteBot 删除机器记录（仅从列表移除，botlog 文件仍保留）
func (s *Store) DeleteBot(id string) error {
	return s.execAffecting(`DELETE FROM bots WHERE id = ?`, id)
}
