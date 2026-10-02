package webui

// 预定（排队）相关的接口：查自己的占用/排队情况，以及主动放弃。

import (
	"net/http"

	"remoteassist-helper/internal/auth"
)

// handleReservations GET /api/reservations
//
// 只返回**当前用户自己**正在占用或排队中的机器；空闲机器不出现在这里。
// idle_deadline > 0 表示服务端正在等他回答「是否下线」，前端可据此本地倒计时。
func (s *Server) handleReservations(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	list := s.m.Reservations(authSubscriber(id))
	writeJSON(w, list)
}

// handleBotRelease POST /api/bots/{id}/release
//
// 主动放弃该机器的控制权：占用者让位给队首，排队者退出队列。
// 对应的浏览器终端连接会随之结束（前端收到 bye 后收起面板）。
func (s *Server) handleBotRelease(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")

	b := s.m.Get(botID)
	if b == nil {
		writeError(w, http.StatusNotFound, "机器不在线: "+botID)
		return
	}
	// 与机器列表同一套可见性口径：无权访问的机器不暴露其占用情况
	if id.Role < s.st.MinRoleOf(botID) {
		writeError(w, http.StatusForbidden, "无权访问该机器")
		return
	}

	released := b.Release(authSubscriber(id))
	s.prog.Info("Web 控制台放弃 bot %s，账号 %s（%s）", botID, id.Username, releasedText(released))
	writeJSON(w, map[string]any{"status": "ok", "released": released})
}

// releasedText 日志用：区分「真的放弃了」与「本来就没占用」
func releasedText(released bool) string {
	if released {
		return "已释放"
	}
	return "无占用"
}
