package webui

// replay_api.go：命令历史回放的保留策略（仅管理员）。
//
// 回放数据 = 各操作上下文按段持久化的终端输出（见 store/context_segments）。
// 管理员在「命令历史回放」面板选择按条数或按天数保留，保存后服务端立即清理
// 多余数据且不可恢复，因此这块存储不会无限增长。

import (
	"net/http"
	"strconv"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/store"
)

// replayView 策略 + 现状 + 取值上限（前端据此做输入校验）
type replayView struct {
	store.ReplaySettings
	Stats         store.ReplayStats `json:"stats"`
	MaxCountLimit int               `json:"max_count_limit"`
	MaxDaysLimit  int               `json:"max_days_limit"`
}

// replayViewOf 组装返回体
func (s *Server) replayViewOf(cfg store.ReplaySettings) replayView {
	st, _ := s.st.ReplayStats()
	return replayView{
		ReplaySettings: cfg,
		Stats:          st,
		MaxCountLimit:  store.MaxReplayCount,
		MaxDaysLimit:   store.MaxReplayDays,
	}
}

// handleReplayGet GET /api/replay/settings
//
// 当前保留策略与回放数据现状（段数 / 占用字节 / 涉及上下文数）。
func (s *Server) handleReplayGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.st.ReplaySettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取回放设置失败: "+err.Error())
		return
	}
	writeJSON(w, s.replayViewOf(cfg))
}

// handleReplaySet POST /api/replay/settings
//
// 保存保留策略并立即按新策略清理多余回放（不可恢复）。
// body: {"mode":"count|days","max_count":N,"max_days":N}
func (s *Server) handleReplaySet(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	var in struct {
		Mode     string `json:"mode"`
		MaxCount int    `json:"max_count"`
		MaxDays  int    `json:"max_days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}

	cfg, err := s.st.SaveReplaySettings(store.ReplaySettings{
		Mode: in.Mode, MaxCount: in.MaxCount, MaxDays: in.MaxDays,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view := s.replayViewOf(cfg)
	detail := "按条数保留，每条上下文最多 " + strconv.Itoa(cfg.MaxCount) + " 段"
	if cfg.Mode == store.ReplayByDays {
		detail = "按天数保留，只留最近 " + strconv.Itoa(cfg.MaxDays) + " 天"
	}
	s.prog.Info("回放保留策略更新为：%s（清理后剩 %d 段 / %d 字节）",
		detail, view.Stats.Segments, view.Stats.Bytes)
	s.audit(id, r, store.ActionReplayPolicy, "", detail)
	writeJSON(w, view)
}
