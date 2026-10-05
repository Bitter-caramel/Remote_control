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
//	GET  /api/bots         在线机器列表（按角色过滤，含上下文数量概况）
//	GET  /api/logs         日志读取（program / bots / botlog）
//	GET  /api/events       接入/接续/释放的播报历史（按机器等级过滤）
//	/api/term              终端桥接（WebSocket，按上下文路由，输入锁独占）
//	见 context_api.go：/api/bots/{id}/ctx、/api/ctx/*（申请 / 授权 / 默认可看）
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
	"sync/atomic"
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

	// 播报
	mux.Handle("GET /api/events", a.RequireAuth(http.HandlerFunc(s.handleEvents)))

	// 操作上下文：列上下文 / 打开自己的上下文 / 申请 / 授权 / 默认可看
	mux.Handle("GET /api/bots/{id}/ctx", a.RequireAuth(http.HandlerFunc(s.handleCtxList)))
	mux.Handle("POST /api/bots/{id}/ctx", a.RequireAuth(http.HandlerFunc(s.handleCtxOpen)))
	mux.Handle("POST /api/ctx/{ctxID}/request", a.RequireAuth(http.HandlerFunc(s.handleCtxRequest)))
	mux.Handle("GET /api/ctx/requests", a.RequireAuth(http.HandlerFunc(s.handleCtxRequests)))
	mux.Handle("POST /api/ctx/requests/{id}/answer", a.RequireAuth(http.HandlerFunc(s.handleCtxAnswer)))
	mux.Handle("GET /api/ctx/grants", a.RequireAuth(http.HandlerFunc(s.handleCtxGrants)))
	mux.Handle("POST /api/ctx/grants/{id}/revoke", a.RequireAuth(http.HandlerFunc(s.handleCtxRevoke)))
	mux.Handle("POST /api/me/watch-default", a.RequireAuth(http.HandlerFunc(s.handleWatchDefault)))

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
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Role         int    `json:"role"`
	RoleName     string `json:"role_name"`
	CanOperate   bool   `json:"can_operate"`
	WatchDefault bool   `json:"watch_default"` // 用户级「默认可看」是否开启
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
	info := newSessionInfo(id)
	info.WatchDefault = s.st.WatchDefault(id.UserID)
	writeJSON(w, info)
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

// wsOutput 服务端 → 浏览器。
// Type 除 ready/output/bye/error 外，还会直接落 core 的控制消息类型
// （granted / revoked / opened / failed），前端按同一字段分派。
type wsOutput struct {
	Type      string `json:"type"`
	Data      string `json:"data"` // output：base64 的终端字节
	Text      string `json:"text"`
	Mode      string `json:"mode"`       // ready：operator | observer（当前是否持有输入锁）
	Role      string `json:"role"`       // ready：owner | operate | watch
	CtxID     string `json:"ctx_id"`     // 本条消息所属的操作上下文
	OwnerName string `json:"owner_name"` // 上下文所有者的展示名
	Holder    string `json:"holder"`     // 当前输入持有者展示名
}

// authSubscriber 把登录身份转成终端接入者身份（权限已由 AccessModeFor 判定）。
func authSubscriber(id *auth.Identity) core.Subscriber {
	display := id.DisplayName
	if display == "" {
		display = id.Username
	}
	return core.Subscriber{
		Kind:     core.SubBrowser,
		UserID:   id.UserID,
		Username: id.Username,
		Display:  display,
	}
}

// coreRole 把 store 的访问模式翻成 core 的订阅角色
func coreRole(mode string) string {
	switch mode {
	case store.ModeOwner:
		return core.SubOwner
	case store.ModeOperate:
		return core.SubOperate
	default:
		return core.SubWatch
	}
}

// recheckEvery 每多少个心跳周期复查一次登录态与权限（1 秒/周期）
const recheckEvery = 5

// byeText 订阅通道被关闭时的结束语：
// 机器下线与「上下文结束 / 授权被收回」都会走到这里，用是否有该机器区分。
func (s *Server) byeText(botID string) string {
	if s.m.Get(botID) == nil {
		return "该机器已下线"
	}
	return "连接已结束"
}

// ctrlOutput 把 core 的控制消息转成下发给浏览器的消息
func ctrlOutput(cm core.ControlMsg, ctxID, role, ownerName string) wsOutput {
	return wsOutput{
		Type:      cm.Type,
		Text:      cm.Text,
		Holder:    cm.Holder,
		CtxID:     ctxID,
		Role:      role,
		OwnerName: ownerName,
	}
}

// flushCtrl 把缓冲里还剩的控制消息发完。
// 被收回输入权时 core 会「先投递 revoked、再关闭输出通道」，
// 两者同时就绪时 select 是随机选的，所以关通道后要补发一次，免得用户看不到原因。
func flushCtrl(sub *core.Subscription, ctxID, role, ownerName string, send func(wsOutput) error) {
	for {
		select {
		case cm := <-sub.Ctrl:
			if err := send(ctrlOutput(cm, ctxID, role, ownerName)); err != nil {
				return
			}
		default:
			return
		}
	}
}

// ctxInputHolder 取某条上下文当前的输入持有者展示名（用于 ready 消息）
func ctxInputHolder(b *core.BOT, ctxID string) string {
	for _, v := range b.CtxViews() {
		if v.ID == ctxID {
			return v.InputName
		}
	}
	return ""
}

// handleTerm 浏览器终端桥接：/api/term?bot=<botID>&ctx=<ctxID>。
//
// 一条连接只说一条操作上下文：owner 实时收发、operate 被授权后可接续、watch 只读按段同步。
// 未带 ctx 时落到「我自己的上下文」（观察者没有上下文，直接拒绝）。
//
// 输入是独占的：只有当前持有输入锁的人（sub.Input 初值 + granted/revoked 跟踪）才能写。
// 长连接不会自动重放鉴权，所以这里周期性复查：会话被撤销、机器等级调高、
// 或该上下文的访问授权被收回时，立即断开。
func (s *Server) handleTerm(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.URL.Query().Get("bot")
	ctxID := r.URL.Query().Get("ctx")

	if botID == "" {
		writeError(w, http.StatusBadRequest, "缺少参数 bot")
		return
	}
	if id.Role < s.st.MinRoleOf(botID) {
		writeError(w, http.StatusForbidden, "无权访问该机器")
		return
	}

	// 没带 ctx：落到我自己的上下文（不存在则创建）；观察者没有上下文可言。
	if ctxID == "" {
		if !id.CanOperate() {
			writeError(w, http.StatusForbidden, "观察者没有自己的操作上下文")
			return
		}
		c, err := s.st.CreateOrGetContext(botID, id.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "创建上下文失败: "+err.Error())
			return
		}
		ctxID = c.ID
	}

	// 先按账号 + 角色判定访问模式，再看机器是否在线（避免对无权者泄露在线状态）
	mode := s.st.AccessModeFor(ctxID, id.UserID, id.Role)
	if mode == store.ModeNone {
		writeError(w, http.StatusForbidden, "无权访问该上下文")
		return
	}
	// 观察者即使被授权也只读，不能拿 owner 级通道
	if !id.CanOperate() {
		mode = store.ModeWatch
	}
	ctx, err := s.st.GetContext(ctxID)
	if err != nil {
		writeError(w, http.StatusNotFound, "上下文不存在或已结束")
		return
	}

	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	b := s.m.Get(botID)
	if b == nil {
		_ = conn.WriteJSON(wsOutput{Type: "error", Text: "机器不在线: " + botID})
		return
	}

	// 补齐内存态里的 owner 信息（被控端遗留的上下文可能还没登记 owner）
	b.EnsureCtx(ctxID, ctx.OwnerID, ctx.OwnerName)

	var wmu sync.Mutex
	send := func(msg wsOutput) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteJSON(msg)
	}

	role := coreRole(mode)
	sub := b.SubscribeCtx(ctxID, authSubscriber(id), role)
	defer sub.Close()
	b.Log().Sys("Web 控制台接入上下文 %s（%s）", ctxID, id.Username)
	s.prog.Info("Web 控制台接入 bot %s 上下文 %s，账号 %s（%s / %s）",
		botID, ctxID, id.Username, store.RoleName(id.Role), role)

	// 只有 owner 负责开启被控端的 shell：watch / operate 复用 owner 建好的会话，
	// 免得观看者接入时把 owner 的终端尺寸带偏（operate 会在获得输入权后自行 resize）。
	// 带上 DB 记录的 cwd：bot 重启后 shell 已消失，被控端据此把新 shell 建在原工作目录。
	if role == core.SubOwner {
		if err := b.Send(&protocol.Message{Type: protocol.TypeCtxOpen, CtxID: ctxID, Cwd: ctx.Cwd}); err != nil {
			return
		}
	}

	// holds：本条连接当前是否持有输入锁；随 granted / revoked 变化
	holds := atomic.Bool{}
	holds.Store(sub.Input)
	holderText := func() string {
		if holds.Load() {
			return "operator"
		}
		return "observer"
	}
	if err := send(wsOutput{
		Type:      "ready",
		Mode:      holderText(),
		Role:      role,
		CtxID:     ctxID,
		OwnerName: sub.OwnerName,
		Holder:    ctxInputHolder(b, ctxID),
	}); err != nil {
		return
	}

	readDone := make(chan struct{})

	// 浏览器 → bot：按键输入 / 尺寸变化。输入按输入锁放行；尺寸只让实时角色改。
	go func() {
		defer close(readDone)
		for {
			var in wsInput
			if err := conn.ReadJSON(&in); err != nil {
				return
			}
			switch in.Type {
			case "input":
				// 只看「当前是否持有输入锁」：观看者被授权接续后，core 会同步把它升级为
				// operate 并推送 granted，holds 随之变 true；握手时冻结的 role 不能再用。
				if !holds.Load() {
					continue
				}
				p, err := protocol.DecodeB64(in.Data)
				if err != nil {
					continue
				}
				if err := b.Send(&protocol.Message{
					Type: protocol.TypeInput, CtxID: ctxID, Data: protocol.EncodeB64(p),
				}); err != nil {
					return
				}
			case "resize":
				// 尺寸只让当前持有输入锁的人改，免得观看者把 owner 的终端尺寸带偏
				if !holds.Load() {
					continue
				}
				if in.Cols > 0 && in.Rows > 0 {
					_ = b.Send(&protocol.Message{
						Type: protocol.TypeResize, CtxID: ctxID, Cols: in.Cols, Rows: in.Rows,
					})
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
		case cm, ok := <-sub.Ctrl:
			if !ok {
				_ = send(wsOutput{Type: "bye", Text: s.byeText(b.ID)})
				return
			}
			// 输入权变化：被授权才可写，被收回立刻转只读
			switch cm.Type {
			case core.CtrlGranted:
				holds.Store(true)
			case core.CtrlRevoked:
				holds.Store(false)
			}
			if err := send(ctrlOutput(cm, ctxID, role, sub.OwnerName)); err != nil {
				return
			}
		case p, ok := <-sub.Out:
			if !ok {
				flushCtrl(sub, ctxID, role, sub.OwnerName, send) // 关通道前可能还压着一条控制消息
				_ = send(wsOutput{Type: "bye", Text: s.byeText(b.ID)})
				return
			}
			if err := send(wsOutput{Type: "output", CtxID: ctxID, Data: protocol.EncodeB64(p)}); err != nil {
				return
			}
		case <-ticker.C:
			ticks++
			if s.m.Get(b.ID) == nil {
				_ = send(wsOutput{Type: "bye", Text: "该机器已下线"})
				return
			}
			if ticks%recheckEvery != 0 {
				continue
			}
			cur := s.auth.RoleOf(id.SID)
			if cur == 0 {
				_ = send(wsOutput{Type: "bye", Text: "登录状态已失效，请重新登录"})
				return
			}
			// 机器等级也重新读取：管理员把门槛调高后，已打开的连接同样要断
			if cur < s.st.MinRoleOf(b.ID) {
				_ = send(wsOutput{Type: "bye", Text: "权限已变更，该连接被关闭"})
				return
			}
			// 授权被收回（或默认可看被关掉）后，已打开的观看连接也要立刻断
			if s.st.AccessModeFor(ctxID, id.UserID, cur) == store.ModeNone {
				_ = send(wsOutput{Type: "bye", Text: "该上下文的访问权限已被收回"})
				return
			}
		}
	}
}
