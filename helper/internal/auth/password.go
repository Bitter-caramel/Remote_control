// Package auth 协助者端控制台的登录鉴权：
//
//	password.go  口令的慢哈希（PBKDF2-HMAC-SHA256 + 每账号独立盐）
//	token.go     会话令牌（HS256 JWT）
//	key.go       JWT 签名密钥的生成与持久化
//	auth.go      登录/登出/身份解析，以及 HTTP 中间件
//
// 口令哈希与令牌签名是两件互不相干的事，各用各的密钥：
// 口令走 PBKDF2（慢、不可逆，只用于比对），令牌走 HMAC-SHA256（快、对称，用于防篡改）。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

const (
	// DefaultIterations PBKDF2 迭代次数。口令必须用慢哈希，
	// 60 万次让离线爆破的代价足够高；单次登录约几百毫秒，对交互频率可以接受。
	DefaultIterations = 600000

	saltLen = 16 // 盐长度（字节）
	keyLen  = 32 // 派生结果长度（字节）

	// MinPasswordLen 口令最小长度
	MinPasswordLen = 6
)

// ErrPasswordTooShort 口令过短
var ErrPasswordTooShort = fmt.Errorf("口令至少需要 %d 位", MinPasswordLen)

// NewSalt 生成一个随机盐（base64 无填充），每个账号独立保存
func NewSalt() (string, error) {
	buf := make([]byte, saltLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机盐失败: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

// Derive 由「口令 + 盐」派生出定长哈希（base64 无填充）
func Derive(password, salt string, iterations int) (string, error) {
	if iterations <= 0 {
		iterations = DefaultIterations
	}
	raw, err := pbkdf2.Key(sha256.New, password, []byte(salt), iterations, keyLen)
	if err != nil {
		return "", fmt.Errorf("派生口令哈希失败: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

// Verify 常量时间比较口令，避免通过响应时间侧信道推断哈希
func Verify(password, salt, want string, iterations int) bool {
	got, err := Derive(password, salt, iterations)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// HashPassword 为新口令生成「哈希、盐、迭代次数」三件套，分别落库
func HashPassword(password string) (hash, salt string, iterations int, err error) {
	if len([]rune(password)) < MinPasswordLen {
		return "", "", 0, ErrPasswordTooShort
	}
	salt, err = NewSalt()
	if err != nil {
		return "", "", 0, err
	}
	hash, err = Derive(password, salt, DefaultIterations)
	if err != nil {
		return "", "", 0, err
	}
	return hash, salt, DefaultIterations, nil
}

// passwordAlphabet 初始口令与重置口令使用的字符集：去掉 0/O/1/l/I 等易混淆字符
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomPassword 生成随机口令（用于初始管理员账号与管理员重置口令）。
// 只在创建/重置的那一刻展示一次，服务端不留明文。
func RandomPassword() (string, error) {
	const n = 14
	buf := make([]byte, n)
	// 用拒绝采样消除取模偏置：256 % 56 != 0，直接取模会让前几个字符偏多
	max := byte(256 - 256%len(passwordAlphabet))
	out := make([]byte, 0, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成随机口令失败: %w", err)
		}
		for _, b := range buf {
			if b >= max {
				continue
			}
			out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}
