package webui

// admin.go：管理员专属接口。
//
//	/api/users*              账号管理：增删、改角色、重置口令、停用启用
//	/api/bots/all            全部登记过的机器（含等级与备注）
//	/api/bots/{id}/min-role  调整机器最低可见角色
//	/api/bots/{id}/note      修改机器备注
//
// 全部经 RequireAdmin 门禁；每一次变更都写审计日志。

import (
	"errors"
	"net/http"
	"strconv"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/store"
)

// ---------- 账号管理 ----------

func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取账号列表失败: "+err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        int    `json:"role"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Role == 0 {
		in.Role = store.RoleObserver
	}
	if !store.ValidRole(in.Role) {
		writeError(w, http.StatusBadRequest, "非法的角色档位")
		return
	}

	// 未指定口令时由服务端生成，只在本次响应里回传一次
	password := in.Password
	generated := ""
	if password == "" {
		pw, err := auth.RandomPassword()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		password, generated = pw, pw
	}
	hash, salt, iterations, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.st.CreateUser(in.Username, in.DisplayName, hash, salt, iterations, in.Role)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			writeError(w, http.StatusConflict, "账号名已存在")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(me, r, store.ActionUserCreate, u.Username, "角色="+store.RoleName(u.Role))
	s.prog.Info("账号 %s 新建账号 %s（%s）", me.Username, u.Username, store.RoleName(u.Role))
	writeJSON(w, map[string]any{"user": u, "password": generated})
}

func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Role int `json:"role"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !store.ValidRole(in.Role) {
		writeError(w, http.StatusBadRequest, "非法的角色档位")
		return
	}
	target, err := s.st.UserByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.guardLastAdmin(target, in.Role != store.RoleAdmin); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SetUserRole(id, in.Role); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(me, r, store.ActionUserRole, target.Username,
		store.RoleName(target.Role)+" → "+store.RoleName(in.Role))
	s.prog.Info("账号 %s 把 %s 的角色改为 %s", me.Username, target.Username, store.RoleName(in.Role))
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.st.UserByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	password := in.Password
	generated := ""
	if password == "" {
		pw, err := auth.RandomPassword()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		password, generated = pw, pw
	}
	hash, salt, iterations, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SetUserPassword(id, hash, salt, iterations); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 改口令后旧令牌已因 token_version 变化而失效，这里再把在线会话一并撤销
	if _, err := s.st.RevokeUserSessions(id); err != nil {
		s.prog.Error("撤销 %s 的会话失败: %v", target.Username, err)
	}
	s.audit(me, r, store.ActionUserPassword, target.Username, "重置口令")
	s.prog.Info("账号 %s 重置了 %s 的口令", me.Username, target.Username)
	writeJSON(w, map[string]any{"status": "ok", "password": generated})
}

func (s *Server) handleUserDisabled(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	target, err := s.st.UserByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.guardLastAdmin(target, in.Disabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SetUserDisabled(id, in.Disabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail := "启用账号"
	if in.Disabled {
		detail = "停用账号"
		if _, err := s.st.RevokeUserSessions(id); err != nil {
			s.prog.Error("撤销 %s 的会话失败: %v", target.Username, err)
		}
	}
	s.audit(me, r, store.ActionUserDisabled, target.Username, detail)
	s.prog.Info("账号 %s %s %s", me.Username, detail, target.Username)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.st.UserByID(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := s.guardLastAdmin(target, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(me, r, store.ActionUserDelete, target.Username, "角色="+store.RoleName(target.Role))
	s.prog.Info("账号 %s 删除了账号 %s", me.Username, target.Username)
	writeJSON(w, map[string]string{"status": "ok"})
}

// guardLastAdmin 阻止把最后一个可用管理员降级/停用/删除，否则系统将无人可管
func (s *Server) guardLastAdmin(target *store.User, harmful bool) error {
	if !harmful || target.Role != store.RoleAdmin || target.Disabled {
		return nil
	}
	n, err := s.st.CountAdmins()
	if err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("至少要保留一个可用的管理员账号")
	}
	return nil
}

// ---------- 机器管理 ----------

// botAdminView 管理员视角的机器：持久信息 + 当前在线状态
type botAdminView struct {
	store.Bot
	Online   bool   `json:"online"`
	Addr     string `json:"addr"`
	Buffered bool   `json:"buffered"`
}

func (s *Server) handleBotsAll(w http.ResponseWriter, r *http.Request) {
	bots, err := s.st.ListBots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取机器列表失败: "+err.Error())
		return
	}
	online := make(map[string]bool, 8)
	addr := make(map[string]string, 8)
	buffered := make(map[string]bool, 8)
	for _, b := range s.m.BotInfos() {
		online[b.ID] = true
		addr[b.ID] = b.Addr
		buffered[b.ID] = b.Buffered
	}
	list := make([]botAdminView, 0, len(bots))
	for _, b := range bots {
		list = append(list, botAdminView{
			Bot:      b,
			Online:   online[b.ID],
			Addr:     addr[b.ID],
			Buffered: buffered[b.ID],
		})
	}
	writeJSON(w, list)
}

func (s *Server) handleBotMinRole(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	var in struct {
		MinRole int `json:"min_role"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !store.ValidRole(in.MinRole) {
		writeError(w, http.StatusBadRequest, "非法的机器等级")
		return
	}
	if err := s.st.SetBotMinRole(botID, in.MinRole); err != nil {
		s.botErr(w, botID, err)
		return
	}
	s.audit(me, r, store.ActionBotMinRole, botID, "最低可见角色="+store.RoleName(in.MinRole))
	s.prog.Info("账号 %s 把机器 %s 的最低可见角色改为 %s",
		me.Username, botID, store.RoleName(in.MinRole))
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleBotNote(w http.ResponseWriter, r *http.Request) {
	me, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	var in struct {
		Note string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.st.SetBotNote(botID, in.Note); err != nil {
		s.botErr(w, botID, err)
		return
	}
	s.audit(me, r, store.ActionBotNote, botID, "备注="+in.Note)
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) botErr(w http.ResponseWriter, botID string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "机器不存在: "+botID)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

// ---------- 小工具 ----------

// pathID 取路径里的 {id} 并转成账号 ID
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "非法的账号 ID")
		return 0, false
	}
	return id, true
}

// audit 写审计记录；失败只记程序日志，不影响业务结果
func (s *Server) audit(me *auth.Identity, r *http.Request, action, target, detail string) {
	if me == nil {
		return
	}
	err := s.st.Audit(store.AuditEntry{
		UserID:   me.UserID,
		Username: me.Username,
		Action:   action,
		Target:   target,
		Detail:   detail,
		IP:       auth.ClientIP(r),
	})
	if err != nil {
		s.prog.Error("写审计日志失败: %v", err)
	}
}
