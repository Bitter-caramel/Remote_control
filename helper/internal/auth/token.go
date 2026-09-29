package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// jwtAlg 固定使用 HS256。校验时严格比对算法名，
// 避免 alg=none / 算法混淆这类降级攻击。
const jwtAlg = "HS256"

// ErrBadToken 令牌格式或签名不合法
var ErrBadToken = errors.New("令牌无效")

// Claims 会话令牌的负载。
//
// 注意：JWT 的负载只是 base64url 编码，任何人拿到都能解开，
// 因此这里只放「会话 ID + 账号 ID + 版本号」这类标识，
// 绝不放口令或口令哈希。签名的意义是防篡改，不是保密。
type Claims struct {
	Sub string `json:"sub"` // 账号名（仅用于排查，鉴权不看它）
	UID int64  `json:"uid"` // 账号 ID
	Ver int64  `json:"ver"` // 签发时的 users.token_version
	SID string `json:"sid"` // 会话 ID（sessions.id）
	Iat int64  `json:"iat"` // 签发时间（Unix 秒）
	Exp int64  `json:"exp"` // 过期时间（Unix 秒）
}

// jwtHeader 固定的头部，预编码避免每次序列化
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// SignToken 用签名密钥签发令牌
func SignToken(key []byte, c Claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + sign(key, body), nil
}

// ParseToken 校验签名并解析令牌；签名不对一律返回 ErrBadToken
func ParseToken(key []byte, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	// 先验算法头，再验签名，最后才解析负载
	head, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrBadToken
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(head, &h); err != nil || !strings.EqualFold(h.Alg, jwtAlg) {
		return nil, ErrBadToken
	}
	if !hmac.Equal([]byte(sign(key, parts[0]+"."+parts[1])), []byte(parts[2])) {
		return nil, ErrBadToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrBadToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrBadToken
	}
	return &c, nil
}

// sign 计算 body 的 HMAC-SHA256 签名（base64url 无填充）
func sign(key []byte, body string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
