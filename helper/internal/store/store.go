// Package store 协助者端的本地持久化层：
// 账号与权限、机器等级、登录会话、审计日志。
//
// 采用 modernc.org/sqlite（纯 Go 实现，不依赖 cgo），
// 因此 build-helper-linux-amd64.bat 的 CGO_ENABLED=0 交叉编译依然成立。
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 角色档位：数值越大权限越高，"能看/能操作" 一律用数值比较
const (
	RoleObserver = 1 // 观察者：只能看，不能输入
	RoleOperator = 2 // 普通用户：可操作机器
	RoleAdmin    = 3 // 管理员：上帝权限
)

// schemaVersion 当前数据库结构版本，写入 meta 表，供后续迁移判断
const schemaVersion = 1

//go:embed schema.sql
var schemaSQL string

// RoleName 角色中文名（前端与 DOS 面板共用同一套文案）
func RoleName(role int) string {
	switch role {
	case RoleAdmin:
		return "管理员"
	case RoleOperator:
		return "普通用户"
	case RoleObserver:
		return "观察者"
	default:
		return "未知"
	}
}

// ValidRole 是否为合法角色档位
func ValidRole(role int) bool { return role >= RoleObserver && role <= RoleAdmin }

// DefaultBotMinRole 新发现的机器默认最低可见角色
const DefaultBotMinRole = RoleOperator

// Store 数据库句柄。所有方法并发安全（内部 database/sql 连接池）。
type Store struct {
	db   *sql.DB
	path string
}

// Open 打开（不存在则创建）数据库并完成建表。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	// WAL 提升并发读写表现，busy_timeout 避免瞬间锁冲突直接报错，
	// foreign_keys 必须每连接打开（SQLite 默认关闭）。
	dsn := "file:" + filepath.ToSlash(path) + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单连接串行化，彻底避免 SQLITE_BUSY；本项目的写入频率远低于此上限。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库
func (s *Store) Close() error { return s.db.Close() }

// Path 数据库文件路径
func (s *Store) Path() string { return s.path }

// migrate 建表并登记结构版本；已存在则原样保留（幂等）。
func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("初始化数据库结构失败: %w", err)
	}
	var cur string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = s.db.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)`,
			strconv.Itoa(schemaVersion))
		return err
	case err != nil:
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}
	return nil
}

// ---------- 时间与布尔的时间戳转换 ----------

func toTS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromTS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
