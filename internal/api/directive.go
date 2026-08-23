package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// Saying something to a run that is already going.
//
// The other way to affect a running task is to cancel it, which stops
// everything. This is the smaller act: change the direction of work in progress,
// or add what to do after it. Both only mean anything for a backend that holds a
// conversation with its agent, which is why the refusal names that rather than
// pretending the request was malformed.

// steerRun records something to say to a running agent.
func (s *Server) steerRun(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Kind == "" {
		input.Kind = "steer"
	}
	if !containsValue(store.RunDirectiveKinds, input.Kind) {
		writeError(w, http.StatusBadRequest, "invalid_directive",
			"지시 종류는 "+strings.Join(store.RunDirectiveKinds, ", ")+" 중 하나여야 합니다.")
		return
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "empty_directive", "에이전트에게 전달할 내용을 입력해 주세요.")
		return
	}
	// The same bound every other prompt has. An agent's context is finite and a
	// directive competes with the work for it.
	if len([]rune(message)) > 4000 {
		writeError(w, http.StatusBadRequest, "directive_too_long", "전달할 내용이 너무 깁니다.")
		return
	}

	runID := chi.URLParam(r, "id")
	admin := u.Role == "admin"
	run, err := s.store.AgentRunByID(r.Context(), runID, u.ID, admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A backend that starts a command and waits for it has nothing to say this
	// to. Accepting it would leave the person watching a directive that is never
	// delivered and never explained.
	if !s.runnerHoldsAConversation(r.Context(), run) {
		writeError(w, http.StatusConflict, "runner_not_conversational",
			"이 실행 방식은 진행 중에 지시를 받지 못합니다. 프로토콜 실행으로 도는 작업에서만 쓸 수 있습니다.")
		return
	}

	directive, err := s.store.AddRunDirective(r.Context(), runID, u.ID, input.Kind, message, admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "run.directive."+input.Kind, "run", runID, "success", clientIP(r),
		map[string]any{"agentId": run.AgentID, "chars": len([]rune(message))})
	writeJSON(w, http.StatusAccepted, directive)
}

// runDirectives lists what has been said to one run and whether it arrived.
func (s *Server) runDirectives(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	runID := chi.URLParam(r, "id")
	if _, err := s.store.AgentRunByID(r.Context(), runID, u.ID, u.Role == "admin"); err != nil {
		writeStoreError(w, err)
		return
	}
	items, err := s.store.RunDirectives(r.Context(), runID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "kinds": store.RunDirectiveKinds})
}

// runnerHoldsAConversation reports whether this run's backend can be spoken to
// while it works.
//
// Asked of the Goal rather than guessed from the runtime type: the same runtime
// may offer several backends, and only one of them keeps a process open.
func (s *Server) runnerHoldsAConversation(ctx context.Context, run store.AgentRun) bool {
	goal, err := s.store.AgentGoalByID(ctx, run.AgentID)
	if err != nil {
		return false
	}
	return goal.Runner == store.RunnerRPC
}
