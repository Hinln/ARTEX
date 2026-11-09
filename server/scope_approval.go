package server

import (
	"net/http"
	"strconv"
)

// 内网目标审批：打进去之后看到的内网是真战果，但同一台机器上的 Docker 靶场长得
// 一模一样。模型登记私网范围时一律挂 pending，由人在这里点头才生效。
func (s *Server) registerScopeApproval(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}/scope/pending", s.listPendingScope)
	mux.HandleFunc("POST /api/tasks/{id}/scope/{sid}/approve", s.approveTaskScope)
	mux.HandleFunc("POST /api/tasks/{id}/scope/{sid}/reject", s.rejectTaskScope)
}

// numericTaskID 把任务字符串 id 转成 tasks.id；不合法就返回 0。
func numericTaskID(t *Task) int64 {
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (s *Server) listPendingScope(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	rows, err := s.m.pg.PendingTaskScope(r.Context(), numericTaskID(t))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": rows})
}

func (s *Server) approveTaskScope(w http.ResponseWriter, r *http.Request) {
	s.decideTaskScope(w, r, true)
}

func (s *Server) rejectTaskScope(w http.ResponseWriter, r *http.Request) {
	s.decideTaskScope(w, r, false)
}

func (s *Server) decideTaskScope(w http.ResponseWriter, r *http.Request, approve bool) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的 scope id")
		return
	}
	var n int64
	if approve {
		n, err = s.m.pg.ApproveTaskScope(r.Context(), numericTaskID(t), sid)
	} else {
		n, err = s.m.pg.RejectTaskScope(r.Context(), numericTaskID(t), sid)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, http.StatusConflict, "这条范围不在待审状态（可能已批准、已作废或已删除）")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": approve, "scope_id": sid})
}
