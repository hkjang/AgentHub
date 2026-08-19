package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// The runtimes, described, and the handover between an autonomous run and a
// person working in one.

// runtimeTypes describes every adapter this build supports.
//
// The console used to carry its own copy of these names and one-line summaries,
// which meant the comparison a person needs when choosing a runtime — what it is
// good at, what it will not do, whether it even has a terminal — lived nowhere,
// and the two copies were already drifting.
func (s *Server) runtimeTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": runtimetype.Descriptors()})
}

// resolveTask closes a task a person took over in the runtime.
//
// Only a handed-off task can be closed this way. Letting anybody mark any task
// completed would make the status meaningless, and leaving no way to close this
// one would keep every handover open forever — which is how a queue stops being
// read at all.
func (s *Server) resolveTask(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		// Status is completed or cancelled: the person either finished the work or
		// decided it should not happen.
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = store.TaskCompleted
	}
	note := strings.TrimSpace(input.Note)
	if note == "" {
		note = "담당자가 런타임에서 이어받아 마무리했습니다."
	}
	if status == store.TaskCancelled && input.Note == "" {
		note = "담당자가 이어받은 뒤 취소했습니다."
	}
	item, err := s.store.ResolveHandoffTask(r.Context(), chi.URLParam(r, "id"), u.ID, status, note, u.Role == "admin")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "not_handed_off", "런타임 인계 상태인 작업만 이렇게 마무리할 수 있습니다.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_resolution", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "task.resolve", "task", item.ID, "success", clientIP(r),
		map[string]any{"status": status, "note": note})
	s.logger.Info("handed-off task resolved by a person", "task", item.ID, "status", status, "by", u.Username)
	writeJSON(w, http.StatusOK, item)
}
