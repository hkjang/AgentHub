package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
	descriptors := runtimetype.Descriptors()
	// What this deployment has actually seen each type do.
	//
	// Fifteen types are offered and every one looks equally available, which is
	// only true where every one has been set up. Somewhere without the image for
	// a type loaded, or without a cluster, they are not equal at all — and the
	// person choosing finds out by creating an agent, pressing start and reading
	// a failure. The platform knows what has happened here and has never put it
	// beside the choice.
	experiences, err := s.store.RuntimeTypeExperiences(r.Context())
	if err != nil {
		// Not knowing the history is not a reason to refuse the list. The choice
		// is still possible; it is only less informed, and saying nothing about a
		// type is what the platform did until now anyway.
		s.logger.Warn("runtime type history could not be read", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"items": descriptors})
		return
	}
	items := make([]map[string]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		entry := map[string]any{}
		body, marshalErr := json.Marshal(descriptor)
		if marshalErr == nil {
			_ = json.Unmarshal(body, &entry)
		}
		entry["experience"] = runtimeExperienceOf(experiences[descriptor.Type])
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// runtimeFailureStatus names the observed states that mean something went wrong.
//
// The list is the operator's, not the platform's bookkeeping: 'created' and
// 'pending' are states a runtime passes through, and 'stopped' is a runtime that
// was told to stop. None of them is a failure, and a verdict that called them one
// would be inventing bad news about a type nobody had trouble with.
func runtimeFailureStatus(status string) bool {
	switch status {
	case "failed", "crashed", "spawn_failed", "unhealthy":
		return true
	}
	return false
}

// runtimeExperienceOf turns what happened into what to say about it.
//
// The verdicts are deliberately about the past rather than the future. "proven"
// means a runtime of this type ran here; it does not promise the next one will,
// because the cluster may have changed since. "untried" is not a warning — most
// deployments will never use most of these — it is the absence of evidence, said
// plainly instead of left to look like approval.
func runtimeExperienceOf(experience store.RuntimeTypeExperience) map[string]any {
	verdict, detail := "untried", "이 배포에서 아직 실행해 본 적이 없습니다."
	switch {
	case experience.Started > 0:
		verdict = "proven"
		detail = fmt.Sprintf("이 배포에서 %d번 실행됐습니다.", experience.Started)
	case experience.LastFailure != "" || runtimeFailureStatus(experience.LastStatus):
		verdict = "failed"
		detail = "마지막 시도가 실패했습니다"
		if experience.LastFailure != "" {
			detail += ": " + experience.LastFailure
		} else {
			detail += fmt.Sprintf("(상태: %s).", experience.LastStatus)
		}
	case experience.Attempts > 0:
		// Created and never started, with nothing recorded as a failure. Calling
		// that failed would be the platform inventing bad news: nothing broke,
		// the runtime was simply never brought up, and somebody choosing needs
		// that difference.
		verdict = "attempted"
		detail = fmt.Sprintf("%d번 만들어졌지만 실행된 적은 없습니다(마지막 상태: %s).", experience.Attempts, experience.LastStatus)
	}
	return map[string]any{
		"verdict": verdict, "detail": detail,
		"attempts": experience.Attempts, "started": experience.Started,
		"lastStatus": experience.LastStatus, "lastAt": experience.LastAt,
		"approvedImages": experience.ApprovedImages,
	}
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
