package webui

// webui：浏览器控制台。helper 启动后即对外开放，同网段的任意设备
// 打开 http://<helper 所在 IP>:<端口>/ 就是完整控制台。
//
// 前端全部内嵌在 helper.exe 里（go:embed），不依赖外网 CDN，也不缺文件。
//
// 鉴权：除静态资源外，所有 /api/* 都必须先登录（httpOnly Cookie 里的会话令牌）。
// 角色分三档 —— 观察者(1) 只能看，普通用户(2) 可操作，管理员(3) 另有账号与机器管理。
// 机器是否可见由 bots.min_role 决定：能看 = 角色 >= min_role。
//
// 路由（挂在主 HTTP mux 上）：
//
//	/                      前端静态资源（公开，登录页也在其中）
//	POST /api/login        登录
//	POST /api/logout       登出（撤销当前会话）
//	GET  /api/me           当前身份
//	GET  /api/bots         在线机器列表（按角色过滤）
//	GET  /api/logs         日志读取（program / bots / botlog）
//	/api/term              终端桥接（WebSocket，与 /attach 等价的浏览器版本）
//	见 admin.go：/api/users*、/api/bots/all、机器分级与备注

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
	"remoteassist-helper/logx"
	"remoteassist-helper/protocol"
)

//go:embed assets
var assetsFS embed.FS

// Server Web 控制台
type Server struct {
	m    *core.Manager
	prog *logx.ProgramLog
	st   *store.Store
	auth *auth.Authenticator
	up   websocket.Upgrader
}

// Register 把 Web 控制台挂到 mux 上
func Register(mux *http.ServeMux, m *core.Manager, prog *logx.ProgramLog,
	st *store.Store, a *auth.Authenticator) *Server {
	s := &Server{m: m, prog: prog, st: st, auth: a}
	s.up = websocket.Upgrader{CheckOrigin: s.sameOrigin}

	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		prog.Error("Web 控制台静态资源不可用: %v", err)
		return s
	}
	mux.Handle("/", http.FileServer(http.FS(static)))

	// 登录态
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.Handle("POST /api/logout", a.RequireAuth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/me", a.RequireAuth(http.HandlerFunc(s.handleMe)))

	// 机器（按身份过滤）
	mux.Handle("GET /api/bots", a.RequireAuth(http.HandlerFunc(s.handleBots)))
	mux.Handle("GET /api/logs", a.RequireAuth(http.HandlerFunc(s.handleLogs)))
	mux.Handle("/api/term", a.RequireAuth(http.HandlerFunc(s.handleTerm)))

	// 机器管理（仅管理员）
	mux.Handle("GET /api/bots/all", a.RequireAdmin(http.HandlerFunc(s.handleBotsAll)))
	mux.Handle("POST /api/bots/{id}/min-role", a.RequireAdmin(http.HandlerFunc(s.handleBotMinRole)))
	mux.Handle("POST /api/bots/{id}/note", a.RequireAdmin(http.HandlerFunc(s.handleBotNote)))

	// 账号管理（仅管理员）
	mux.Handle("GET /api/users", a.RequireAdmin(http.HandlerFunc(s.handleUserList)))
	mux.Handle("POST /api/users", a.RequireAdmin(http.HandlerFunc(s.handleUserCreate)))
	mux.Handle("POST /api/users/{id}/role", a.RequireAdmin(http.HandlerFunc(s.handleUserRole)))
	mux.Handle("POST /api/users/{id}/password", a.RequireAdmin(http.HandlerFunc(s.handleUserPassword)))
	mux.Handle("POST /api/users/{id}/disabled", a.RequireAdmin(http.HandlerFunc(s.handleUserDisabled)))
	mux.Handle("DELETE /api/users/{id}", a.RequireAdmin(http.HandlerFunc(s.handleUserDelete)))

	prog.Info("Web 控制台已挂载（登录鉴权已启用）")
	return s
}

// sameOrigin 收紧 WebSocket 的跨站限制：
// 非浏览器的 WebSocket 客户端不带 Origin（放行），带 Origin 的必须与请求同源。
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// ---------- 登录态 ----------

// sessionInfo 下发给前端的当前身份
type sessionInfo struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        int    `json:"role"`
	RoleName    string `json:"role_name"`
	CanOperate  bool   `json:"can_operate"`
}

func newSessionInfo(id *auth.Identity) sessionInfo {
	return sessionInfo{
		Username:    id.Username,
		DisplayName: id.DisplayName,
		Role:        id.Role,
		RoleName:    store.RoleName(id.Role),
		CanOperate:  id.CanOperate(),
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	writeJSON(w, newSessionInfo(id))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	ip := auth.ClientIP(r)
	token, id, err := s.auth.Login(in.Username, in.Password, ip, r.UserAgent())
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrDisabled) {
			status = http.StatusForbidden
		}
		s.prog.Info("登录失败: 账号=%s IP=%s 原因=%s",
			store.NormalizeUsername(in.Username), ip, err.Error())
		writeError(w, status, err.Error())
		return
	}
	s.auth.SetSessionCookie(w, token)
	s.prog.Info("登录成功: 账号=%s 角色=%s IP=%s", id.Username, store.RoleName(id.Role), ip)
	writeJSON(w, newSessionInfo(id))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := s.auth.Logout(id, auth.ClientIP(r)); err != nil {
		s.prog.Error("登出失败: %v", err)
	}
	auth.ClearSessionCookie(w)
	writeJSON(w, map[string]string{"status": "ok"})
}

// ---------- 机器列表（按角色过滤） ----------

func (s *Server) handleBots(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	minRoles, err := s.st.MinRoles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取机器等级失败: "+err.Error())
		return
	}
	list := s.m.BotInfos()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	visible := make([]core.BotInfo, 0, len(list))
	for _, b := range list {
		min, ok := minRoles[b.ID]
		if !ok {
			min = store.DefaultBotMinRole
		}
		if id.Role >= min {
			visible = append(visible, b)
		}
	}
	writeJSON(w, visible)
}

// ---------- 日志 ----------

// handleLogs 读取日志：?kind=program|bots|bot[&id=<botID>]
//
// 权限：程序日志任意已登录身份可看；机器总表与单机录像按机器等级门禁
// （bots.log 含全部连过的机器，属普通用户起步）。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	q := r.URL.Query()

	var (
		text string
		err  error
	)
	switch q.Get("kind") {
	case "program":
		text, err = s.prog.Read()
	case "bots":
		if id.Role < store.RoleOperator {
			writeError(w, http.StatusForbidden, "观察者无权查看机器总表")
			return
		}
		text, err = s.m.BotsLog().Read()
	case "bot":
		botID := q.Get("id")
		if botID == "" {
			writeError(w, http.StatusBadRequest, "缺少参数 id")
			return
		}
		if id.Role < s.st.MinRoleOf(botID) {
			writeError(w, http.StatusForbidden, "无权查看该机器的终端录像")
			return
		}
		text, err = logx.ReadBotLog(s.m.LogDir(), botID)
	default:
		writeError(w, http.StatusBadRequest, "未知的日志类型")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"text": text})
}

// ---------- 小工具 ----------

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSONStatus(w, code, map[string]string{"error": msg})
}

// decodeBody 解析小型 JSON 请求体（限制 4KB，避免被塞大包）
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	return true
}

// ---------- 浏览器终端桥接 ----------

// wsInput 浏览器 → 服务端
type wsInput struct {
	Type string `json:"type"` // input | resize
	Data string `json:"data"` // input：base64 编码的按键字节
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// wsOutput 服务端 → 浏览器
type wsOutput struct {
	Type string `json:"type"` // output | bye | error
	Data string `json:"data"`
}

// recheckEvery 每多少个心跳周期复查一次登录态与权限（1 秒/周期）
const recheckEvery = 5

// handleTerm 浏览器终端桥接：与 ipc 的 /attach 走同一套 core 订阅广播，
// 因此多个浏览器可以同时看同一台机器、也都能输入（观察者除外）。
//
// 长连接不会自动重放鉴权，所以这里周期性复查：
// 会话被撤销（登出/被踢/停用/改密码）或权限降到看不到这台机器时，立即断开。
func (s *Server) handleTerm(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.URL.Query().Get("bot")

	minRole := s.st.MinRoleOf(botID)
	if id.Role < minRole {
		writeError(w, http.StatusForbidden, "无权访问该机器")
		return
	}

	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	b := s.m.Get(botID)
	if b == nil {
		_ = conn.WriteJSON(wsOutput{Type: "error", Data: "机器不在线: " + botID})
		return
	}

	var wmu sync.Mutex
	send := func(msg wsOutput) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteJSON(msg)
	}

	out, cancel := b.Subscribe()
	defer cancel()
	b.Log().Sys("Web 控制台接入 (%s)", id.Username)
	s.prog.Info("Web 控制台接入 bot %s，账号 %s（%s）",
		botID, id.Username, store.RoleName(id.Role))

	readDone := make(chan struct{})

	// 浏览器 → bot：按键输入 / 尺寸变化。
	// 观察者（角色 1）一律丢弃：能看不能动是这一档的全部含义。
	go func() {
		defer close(readDone)
		for {
			var in wsInput
			if err := conn.ReadJSON(&in); err != nil {
				return
			}
			if !id.CanOperate() {
				continue
			}
			switch in.Type {
			case "input":
				p, err := protocol.DecodeB64(in.Data)
				if err != nil {
					continue
				}
				if err := b.Send(&protocol.Message{Type: protocol.TypeInput, Data: protocol.EncodeB64(p)}); err != nil {
					return
				}
			case "resize":
				if in.Cols > 0 && in.Rows > 0 {
					_ = b.Send(&protocol.Message{Type: protocol.TypeResize, Cols: in.Cols, Rows: in.Rows})
				}
			}
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-readDone:
			b.Log().Sys("Web 控制台断开")
			return
		case p, ok := <-out:
			if !ok {
				_ = send(wsOutput{Type: "bye", Data: "该机器已下线"})
				return
			}
			if err := send(wsOutput{Type: "output", Data: protocol.EncodeB64(p)}); err != nil {
				return
			}
		case <-ticker.C:
			ticks++
			if s.m.Get(b.ID) == nil {
				_ = send(wsOutput{Type: "bye", Data: "该机器已下线"})
				return
			}
			if ticks%recheckEvery != 0 {
				continue
			}
			role := s.auth.RoleOf(id.SID)
			if role == 0 {
				_ = send(wsOutput{Type: "bye", Data: "登录状态已失效，请重新登录"})
				return
			}
			// 机器等级也重新读取：管理员把门槛调高后，已打开的连接同样要断
			if role < s.st.MinRoleOf(b.ID) {
				_ = send(wsOutput{Type: "bye", Data: "权限已变更，该连接被关闭"})
				return
			}
		}
	}
}
