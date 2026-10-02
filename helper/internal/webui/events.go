package webui

// 播报：占用 / 排队 / 释放的历史记录（内存环形缓冲，helper 重启即清空）。
// 只有点进面板才会拉取，不做常驻推送。

import (
	"net/http"
	"strconv"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
)

// handleEvents GET /api/events?limit=100
//
// 可见性与机器列表一致：角色 >= 该机器的 min_role 才能看到它的记录。
// **先按等级全量过滤、后截断**，否则低角色用户会被无权记录顶掉配额；
// 被过滤掉的记录静默丢弃，不提示「已隐藏 N 条」。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit 必须是正整数")
			return
		}
		limit = n
	}
	if limit > eventLimitMax {
		limit = eventLimitMax
	}

	minRoles, err := s.st.MinRoles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取机器等级失败: "+err.Error())
		return
	}

	out := make([]core.Event, 0, limit)
	for _, ev := range s.m.Events().Snapshot() { // 由新到旧
		min, ok := minRoles[ev.BotID]
		if !ok {
			min = store.DefaultBotMinRole
		}
		if id.Role < min {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, out)
}

// eventLimitMax 单次最多返回的播报条数
const eventLimitMax = 500
