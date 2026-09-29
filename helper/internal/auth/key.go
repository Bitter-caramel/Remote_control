package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keyLenBytes JWT 签名密钥长度（HMAC-SHA256 用 32 字节足够）
const keyLenBytes = 32

// LoadOrCreateKey 读取 JWT 签名密钥文件，不存在则生成并落盘（权限 0600）。
//
// 密钥必须跨重启保持不变：一旦更换，所有已登录用户的令牌会立即失效。
// 「让某个用户下线」应该走撤销会话（sessions.revoked_at），而不是丢弃密钥。
func LoadOrCreateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decErr != nil || len(key) < keyLenBytes {
			return nil, fmt.Errorf("签名密钥文件 %s 内容无效，请删除后重启以重新生成", path)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("读取签名密钥失败: %w", err)
	}

	key := make([]byte, keyLenBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成签名密钥失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建密钥目录失败: %w", err)
		}
	}
	text := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return nil, fmt.Errorf("写入签名密钥失败: %w", err)
	}
	return key, nil
}
