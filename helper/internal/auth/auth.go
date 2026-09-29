package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"remoteassist-helper/internal/store"
)

const (
	// CookieName 会话 Cookie 名
	CookieName = "ra_session"
	// SessionTTL 会话默认有效期
	SessionTTL = 12 * time.Hour
	// userAgentLimit 审计里 User-Agent 的截断长度
	userAgentLimit = 200
)

// ErrBadCredentials 账号或口令错误。
// 账号不存在与口令错误共用同一个错误，避免被用来枚举账号。
var ErrBadCredentials = errors.New("账号或口令错误")

// ErrDisabled 账号已被停用
var ErrDisabled = errors.New("账号已被停用")

// dummySalt 账号不存在时用它做一次等价耗时的派生，
// 让「账号不存在」与「口令错误」的响应时间一致。
const dummySalt = "UmVtb3RlQXNzaXN0LWR1bW15LXNhbHQ="

// Identity 已通过鉴权的请求身份。
// Role 每次都从数据库现取，因此管理员降权后无需重新登录即刻生效。
type Identity struct {
	UserID      int64
	Username    string
	DisplayName string
	Role        int
	SID         string
}

// CanOperate 能否对机器执行操作（观察者只能看）
func (i *Identity) CanOperate() bool { return i.Role >= store.RoleOperator }

// IsAdmin 是否管理员
func (i *Identity) IsAdmin() bool { return i.Role >= store.RoleAdmin }

// Authenticator 登录鉴权器：签发/校验令牌，并解析请求身份
type Authenticator struct {
	st  *store.Store
	key []byte // JWT 签名密钥（与口令哈希无关）
	ttl time.Duration
}

// New 构造鉴权器；ttl <= 0 时使用 SessionTTL
func New(st *store.Store, key []byte, ttl time.Duration) *Authenticator {
	if ttl <= 0 {
		ttl = SessionTTL
	}
	return &Authenticator{st: st, key: key, ttl: ttl}
}

// Store 数据层（webui 需要读账号列表与机器等级）
func (a *Authenticator) Store() *store.Store { return a.st }

// TTL 会话有效期
func (a *Authenticator) TTL() time.Duration { return a.ttl }

// Login 校验账号口令，登记会话并签发令牌。
// 口令永远不会写进令牌——令牌里只有会话 ID 与账号 ID。
func (a *Authenticator) Login(username, password, ip, ua string) (string, *Identity, error) {
	acct, err := a.st.AccountByUsername(username)
	if err != nil {
		_, _ = Derive(password, dummySalt, DefaultIterations) // 对齐耗时
		a.auditLoginFailed(username, ip)
		return "", nil, ErrBadCredentials
	}
	if acct.Disabled {
		a.auditLoginFailed(username, ip)
		return "", nil, ErrDisabled
	}
	if !Verify(password, acct.Salt, acct.PasswordHash, acct.Iterations) {
		a.auditLoginFailed(username, ip)
		return "", nil, ErrBadCredentials
	}

	now := time.Now()
	sid, err := newSID()
	if err != nil {
		return "", nil, err
	}
	sess := store.Session{
		ID:           sid,
		UserID:       acct.ID,
		TokenVersion: acct.TokenVersion,
		CreatedAt:    now,
		ExpiresAt:    now.Add(a.ttl),
		IP:           ip,
		UserAgent:    truncate(ua, userAgentLimit),
	}
	if err := a.st.CreateSession(sess); err != nil {
		return "", nil, fmt.Errorf("登记会话失败: %w", err)
	}
	token, err := SignToken(a.key, Claims{
		Sub: acct.Username,
		UID: acct.ID,
		Ver: acct.TokenVersion,
		SID: sid,
		Iat: now.Unix(),
		Exp: sess.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", nil, err
	}

	_ = a.st.MarkUserLogin(acct.ID)
	_ = a.st.Audit(store.AuditEntry{
		UserID: acct.ID, Username: acct.Username,
		Action: store.ActionLogin, IP: ip, Detail: "登录成功",
	})
	return token, &Identity{
		UserID: acct.ID, Username: acct.Username, DisplayName: acct.DisplayName,
		Role: acct.Role, SID: sid,
	}, nil
}

// Logout 撤销当前会话：该令牌立即失效，其他用户完全不受影响
func (a *Authenticator) Logout(id *Identity, ip string) error {
	if id == nil || id.SID == "" {
		return nil
	}
	if err := a.st.RevokeSession(id.SID); err != nil {
		return err
	}
	_ = a.st.Audit(store.AuditEntry{
		UserID: id.UserID, Username: id.Username,
		Action: store.ActionLogout, IP: ip,
	})
	return nil
}

// Identify 从请求 Cookie 解析身份。
// 三道校验缺一不可：签名有效 → 会话未撤销未过期 → 账号未停用且令牌版本一致。
func (a *Authenticator) Identify(r *http.Request) (*Identity, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, ErrBadCredentials
	}
	claims, err := ParseToken(a.key, c.Value)
	if err != nil {
		return nil, ErrBadCredentials
	}
	now := time.Now()
	if claims.Exp <= now.Unix() {
		return nil, ErrBadCredentials
	}
	sess, err := a.st.SessionByID(claims.SID)
	if err != nil || !sess.Active(now) {
		return nil, ErrBadCredentials
	}
	if sess.UserID != claims.UID || sess.TokenVersion != claims.Ver {
		return nil, ErrBadCredentials
	}
	u, err := a.st.UserByID(claims.UID)
	if err != nil || u.Disabled || u.TokenVersion != claims.Ver {
		return nil, ErrBadCredentials
	}
	return &Identity{
		UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Role: u.Role, SID: claims.SID,
	}, nil
}

// Valid 复查某个会话当前是否仍然有效。
// 浏览器终端是长连接，握手之后鉴权不会自动重放，因此桥接协程需要周期性调用它：
// 这样管理员「踢下线」、停用账号、改权限都能立刻作用到已经打开的控制台上。
func (a *Authenticator) Valid(sid string) bool {
	if sid == "" {
		return false
	}
	now := time.Now()
	sess, err := a.st.SessionByID(sid)
	if err != nil || !sess.Active(now) {
		return false
	}
	u, err := a.st.UserByID(sess.UserID)
	if err != nil || u.Disabled {
		return false
	}
	return u.TokenVersion == sess.TokenVersion
}

// RoleOf 返回该会话当前的角色（长连接期间复查用）；会话失效时返回 0
func (a *Authenticator) RoleOf(sid string) int {
	sess, err := a.st.SessionByID(sid)
	if err != nil || !sess.Active(time.Now()) {
		return 0
	}
	u, err := a.st.UserByID(sess.UserID)
	if err != nil || u.Disabled || u.TokenVersion != sess.TokenVersion {
		return 0
	}
	return u.Role
}

// ---------- Cookie ----------

// SetSessionCookie 下发会话 Cookie。
// 当前未启用 TLS，所以 Secure 只能为 false（否则浏览器在 http 下不会回传）；
// 一旦将来切换 https，这里要同步打开 Secure。
func (a *Authenticator) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

// ClearSessionCookie 清除会话 Cookie
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// ---------- 中间件 ----------

type ctxKey struct{}

// FromContext 取出中间件放入的当前身份
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(*Identity)
	return id, ok
}

// RequireAuth 拦截未登录/会话失效的请求，放行时把身份注入上下文
func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Identify(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "未登录或会话已失效")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// RequireRole 要求至少 min 档角色（含登录校验）
func (a *Authenticator) RequireRole(min int, next http.Handler) http.Handler {
	return a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		if id == nil || id.Role < min {
			writeError(w, http.StatusForbidden, "当前身份无权执行该操作")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// RequireAdmin 仅管理员可访问
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return a.RequireRole(store.RoleAdmin, next)
}

// ---------- 小工具 ----------

func (a *Authenticator) auditLoginFailed(username, ip string) {
	_ = a.st.Audit(store.AuditEntry{
		Username: store.NormalizeUsername(username),
		Action:   store.ActionLoginFailed,
		IP:       ip,
		Detail:   "账号或口令错误",
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func newSID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话 ID 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ClientIP 取客户端 IP（仅用于审计展示，不做任何信任判断）
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
