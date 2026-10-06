package feat

// 供功能插件复用的 HTTP 小工具。webui 主包通过别名使用同一份实现
// （见 server.go 顶部的 var 别名），避免同样的工具函数在两处维护。

import (
	"encoding/json"
	"net/http"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/store"
)

// WriteJSON 写 200 JSON
func WriteJSON(w http.ResponseWriter, v any) { WriteJSONStatus(w, http.StatusOK, v) }

// WriteJSONStatus 以指定状态码写 JSON
func WriteJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError 写错误 JSON
func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteJSONStatus(w, code, map[string]string{"error": msg})
}

// DecodeBody 解析小型 JSON 请求体（限制 4KB，避免被塞大包）
func DecodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "请求格式错误")
		return false
	}
	return true
}

// Audit 写审计记录；失败只记程序日志，不影响业务结果。
// 功能包在 handler 里调用 d.Audit(id, r, action, target, detail)。
func (d Deps) Audit(me *auth.Identity, r *http.Request, action, target, detail string) {
	if me == nil {
		return
	}
	err := d.St.Audit(store.AuditEntry{
		UserID:   me.UserID,
		Username: me.Username,
		Action:   action,
		Target:   target,
		Detail:   detail,
		IP:       auth.ClientIP(r),
	})
	if err != nil {
		d.Prog.Error("写审计日志失败: %v", err)
	}
}