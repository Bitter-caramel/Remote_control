package webui

// context_api.go：操作上下文（Ctx）的 HTTP 接口。
//
// 一条上下文 = 一个用户在一台 bot 上的一条独立操作线，默认私密。
// 这里只做「数据 + 权限」的事：列我能看到的上下文、打开我自己的上下文、
// 发起/处理观看或接续申请、收回授权、设置用户级「默认可看」。
// 真正的输出路由与输入锁在 core 层（见 core/context.go），本层判定完把结论传进去。

import (
	"net/http"
	"strconv"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
)

// maxContextsPerBot 单台 bot 上允许同时存在的上下文上限。
// 与被控端 user 侧的硬上限保持一致：这里先拦一道，给出即时反馈，
// 被控端仍会再拦一次（防止绕过本接口直接开上下文）。
const maxContextsPerBot = 5

// ctxView 上下文 + 当前请求者对它的访问模式（外加内存态的运行概况）
type ctxView struct {
	store.Context
	Mode      string `json:"mode"`       // owner | operate | watch | none
	Subs      int    `json:"subs"`       // 当前接入人数（机器在线时有意义）
	InputName string `json:"input_name"` // 当前输入持有者展示名
}

// handleCtxList GET /api/bots/{id}/ctx
//
// 列出该机器上的全部上下文：我自己的 + 别人授权给我的 + 别人开了默认可看的，
// 以及**没权限访问的**（mode=none，仅用于前端展示「申请观看」入口，
// 这类上下文不回传工作目录与标题，避免泄露）。
// mine 是我自己的上下文 ID（没有则为空字符串）。
func (s *Server) handleCtxList(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	if id.Role < s.st.MinRoleOf(botID) {
		writeError(w, http.StatusForbidden, "无权访问该机器")
		return
	}

	all, err := s.st.ListContextsForBot(botID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取上下文失败: "+err.Error())
		return
	}
	// 机器在线时补上内存态信息（接入人数 / 输入持有者）
	runtime := map[string]core.CtxView{}
	if b := s.m.Get(botID); b != nil {
		for _, v := range b.CtxViews() {
			runtime[v.ID] = v
		}
	}

	out := make([]ctxView, 0, len(all))
	mine := ""
	for _, c := range all {
		m := s.st.AccessModeFor(c.ID, id.UserID, id.Role)
		if !id.CanOperate() && m != store.ModeNone {
			m = store.ModeWatch // 观察者无论被怎么授权都只读
		}
		if c.OwnerID == id.UserID {
			mine = c.ID
		}
		if m == store.ModeNone {
			c.Cwd, c.Title = "", "" // 无权限：不暴露工作目录与标题
		}
		v := ctxView{Context: c, Mode: m}
		if rv, ok := runtime[c.ID]; ok {
			v.Subs = rv.Subs
			v.InputName = rv.InputName
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{
		"contexts":    out,
		"mine":        mine,
		"can_operate": id.CanOperate(),
		"max":         maxContextsPerBot,
	})
}

// handleCtxOpen POST /api/bots/{id}/ctx
//
// 打开（不存在则创建）我在该机器上的上下文。幂等：已存在直接返回。
// 超出配额时拒绝——真正兜底在被控端，这里只是提前给出可读的错误。
func (s *Server) handleCtxOpen(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	if !id.CanOperate() {
		writeError(w, http.StatusForbidden, "观察者无法创建操作上下文")
		return
	}
	if id.Role < s.st.MinRoleOf(botID) {
		writeError(w, http.StatusForbidden, "无权访问该机器")
		return
	}

	if c, err := s.st.GetContext(store.CtxID(botID, id.UserID)); err == nil {
		writeJSON(w, c) // 已有：直接复用
		return
	}

	all, err := s.st.ListContextsForBot(botID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取上下文失败: "+err.Error())
		return
	}
	if len(all) >= maxContextsPerBot {
		writeError(w, http.StatusConflict,
			"该机器上的上下文数量已达上限（"+strconv.Itoa(maxContextsPerBot)+"），请等待其他用户释放后再试")
		return
	}

	c, err := s.st.CreateOrGetContext(botID, id.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建上下文失败: "+err.Error())
		return
	}
	s.prog.Info("打开操作上下文 bot=%s ctx=%s 账号=%s", botID, c.ID, id.Username)
	s.audit(id, r, store.ActionCtxOpen, c.ID, "打开操作上下文")
	writeJSON(w, c)
}

// handleCtxRequest POST /api/ctx/{ctxID}/request
//
// 对某条上下文发起观看（watch）或接续（operate）申请。body: {"mode":"watch|operate"}
func (s *Server) handleCtxRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	ctxID := r.PathValue("ctxID")

	var in struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !store.ValidGrantMode(in.Mode) {
		writeError(w, http.StatusBadRequest, "非法的申请模式")
		return
	}
	if in.Mode == store.ModeOperate && !id.CanOperate() {
		writeError(w, http.StatusForbidden, "观察者无法申请接续操作")
		return
	}

	req, err := s.st.CreateRequest(ctxID, id.UserID, in.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(id, r, store.ActionCtxRequest, ctxID, "申请 "+in.Mode)
	writeJSON(w, req)
}

// handleCtxRequests GET /api/ctx/requests
//
// 我作为 owner 的待处理申请（别人想观看或接续我的上下文）。
func (s *Server) handleCtxRequests(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	list, err := s.st.ListPendingRequests(id.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取申请失败: "+err.Error())
		return
	}
	writeJSON(w, list)
}

// handleCtxAnswer POST /api/ctx/requests/{id}/answer
//
// 同意 / 拒绝一条申请（body: {"accept":true|false}）。同意即建立长期授权；
// 若申请的是 operate，同时把输入锁移交给申请者，原持有者转为只读并收到提示。
func (s *Server) handleCtxAnswer(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	reqID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "非法的申请 ID")
		return
	}
	var in struct {
		Accept bool `json:"accept"`
	}
	if !decodeBody(w, r, &in) {
		return
	}

	pending, err := s.st.RequestByID(reqID)
	if err != nil {
		writeError(w, http.StatusNotFound, "申请不存在")
		return
	}
	if pending.OwnerID != id.UserID && !id.IsAdmin() {
		writeError(w, http.StatusForbidden, "只有上下文所有者可以处理该申请")
		return
	}

	req, err := s.st.AnswerRequest(reqID, in.Accept)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Accept && req.Mode == store.ModeOperate {
		s.grantOperate(req)
	}
	detail := "拒绝 " + req.Mode
	if in.Accept {
		detail = "同意 " + req.Mode
	}
	s.audit(id, r, store.ActionCtxAnswer, req.ContextID, detail+"（申请人 "+req.Requester+"）")
	writeJSON(w, req)
}

// handleCtxGrants GET /api/ctx/grants
//
// 我已发出的授权（我拥有的上下文上，别人拿到的手牌）。
func (s *Server) handleCtxGrants(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	list, err := s.st.ListGrants(id.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取授权失败: "+err.Error())
		return
	}
	writeJSON(w, list)
}

// handleCtxRevoke POST /api/ctx/grants/{id}/revoke
//
// 收回一条授权。operate 授权被收回时，输入锁立即回到 owner；
// 观看者连接会在自身权限复查（≤5 秒）后断开。
func (s *Server) handleCtxRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	gid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "非法的授权 ID")
		return
	}
	g, err := s.st.GrantByID(gid)
	if err != nil {
		writeError(w, http.StatusNotFound, "授权不存在")
		return
	}
	ctx, err := s.st.GetContext(g.ContextID)
	if err != nil {
		writeError(w, http.StatusNotFound, "上下文不存在")
		return
	}
	if ctx.OwnerID != id.UserID && !id.IsAdmin() {
		writeError(w, http.StatusForbidden, "只有上下文所有者可以收回授权")
		return
	}

	if err := s.st.RevokeGrant(g.ContextID, g.GranteeID); err != nil {
		writeError(w, http.StatusInternalServerError, "收回授权失败: "+err.Error())
		return
	}
	if g.Mode == store.ModeOperate {
		if b := s.m.Get(g.BotID); b != nil {
			b.EnsureCtx(g.ContextID, ctx.OwnerID, ctx.OwnerName)
			b.RevokeGrantee(g.ContextID, g.GranteeID)
		}
	}
	s.prog.Info("收回上下文授权 ctx=%s 被授权人=%s 模式=%s", g.ContextID, g.Grantee, g.Mode)
	s.audit(id, r, store.ActionCtxRevoke, g.ContextID,
		"收回 "+g.Mode+"（"+g.Grantee+"）")
	writeJSON(w, map[string]any{"status": "ok"})
}

// handleWatchDefault POST /api/me/watch-default
//
// 用户级「默认可看」总开关：开启后我名下所有上下文默认对所有人开放只读。
func (s *Server) handleWatchDefault(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	var in struct {
		On bool `json:"on"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.st.SetWatchDefault(id.UserID, in.On); err != nil {
		writeError(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	detail := "关闭默认可看"
	if in.On {
		detail = "开启默认可看"
	}
	s.audit(id, r, store.ActionWatchDefault, "", detail)
	writeJSON(w, map[string]any{"watch_default": in.On})
}

// ---------- 内部工具 ----------

// grantOperate 同意 operate 申请后把输入锁移交给申请者。
// 需要先把上下文补进内存态，否则 TakeInput 找不到目标会静默失败。
func (s *Server) grantOperate(req *store.ContextRequest) {
	b := s.m.Get(req.BotID)
	if b == nil {
		return // 机器不在线：授权已入库，等对方接入时按 DB 判定
	}
	ownerName := ""
	if c, err := s.st.GetContext(req.ContextID); err == nil {
		ownerName = c.OwnerName
	}
	b.EnsureCtx(req.ContextID, req.OwnerID, ownerName)
	b.TakeInput(req.ContextID, requesterSub(req))
}

// requesterSub 把申请记录里的申请人还原成 core 的接入者身份
func requesterSub(req *store.ContextRequest) core.Subscriber {
	display := req.Requester
	if display == "" {
		display = "用户"
	}
	return core.Subscriber{
		Kind:     core.SubBrowser,
		UserID:   req.RequesterID,
		Username: req.Requester,
		Display:  display,
	}
}
