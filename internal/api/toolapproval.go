package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// The control plane side of the tool approval gate.
//
// These two routes are called by the MCP gateway inside a runtime Pod, not by a
// browser: the caller presents the runtime's own token, whose hash the control
// plane stored when it created the Pod's Secret. That is the whole
// authentication, and it is enough — a token identifies exactly one runtime, and
// everything the request may touch is derived from that runtime rather than taken
// from the request body.
//
// They sit outside the session-authenticated group for the same reason the
// webhook route does, and are equally not open: without a valid runtime token
// they answer 401.

// maxToolArgumentChars bounds what the gateway may send for review. The gateway
// already trims; this is the server refusing to store an unbounded blob.
const maxToolArgumentChars = 4000

// runtimeFromGatewayToken identifies the runtime behind a gateway request.
func (s *Server) runtimeFromGatewayToken(r *http.Request) (store.Runtime, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return store.Runtime{}, false
	}
	runtime, err := s.store.RuntimeByGatewayToken(r.Context(), strings.TrimSpace(header[7:]))
	if err != nil {
		return store.Runtime{}, false
	}
	return runtime, true
}

// requestToolApproval parks one tool call until a person decides.
func (s *Server) requestToolApproval(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.runtimeFromGatewayToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Runtime 토큰을 확인할 수 없습니다.")
		return
	}
	var input struct {
		RuntimeID string `json:"runtimeId"`
		Server    string `json:"server"`
		Tool      string `json:"tool"`
		Arguments string `json:"arguments"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// The body's runtime id is a cross-check, never the authority: the token is.
	if input.RuntimeID != "" && input.RuntimeID != runtime.ID {
		writeError(w, http.StatusForbidden, "runtime_mismatch", "다른 Runtime의 승인을 요청할 수 없습니다.")
		return
	}
	server, tool := strings.TrimSpace(input.Server), strings.TrimSpace(input.Tool)
	if server == "" || tool == "" {
		writeError(w, http.StatusBadRequest, "invalid_tool_approval", "MCP 서버와 도구 이름이 필요합니다.")
		return
	}
	arguments := input.Arguments
	if len(arguments) > maxToolArgumentChars {
		arguments = arguments[:maxToolArgumentChars] + "\n… (이하 생략)"
	}

	agent, err := s.store.AgentByID(r.Context(), runtime.AgentID, runtime.OwnerID, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	reason := agent.Name + " 에이전트가 " + server + " 서버의 " + tool + " 도구 실행 승인을 요청했습니다."
	saved, err := s.store.CreateToolApproval(r.Context(), store.ToolApproval{
		RuntimeID: runtime.ID, AgentID: agent.ID, OwnerID: runtime.OwnerID,
		ServerName: server, ToolName: tool, Arguments: arguments,
	}, reason)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Nobody is watching a Pod wait, so the decision is asked for out loud. The
	// reviewer sees it in 검토 · 승인; the owner is told because it is their agent
	// that is blocked.
	if err := s.store.CreateNotification(r.Context(), runtime.OwnerID, "approval",
		"도구 실행 승인이 필요합니다", reason, "/reviews"); err != nil {
		s.logger.Warn("tool approval notification not delivered", "runtime", runtime.ID, "error", err)
	}
	s.store.Audit(r.Context(), nil, "tool.approval.request", "runtime", runtime.ID, "pending", clientIP(r),
		map[string]any{"server": server, "tool": tool, "agentId": agent.ID, "approvalId": saved.ApprovalID})
	s.logger.Info("tool call is waiting for approval", "runtime", runtime.ID, "agent", agent.ID, "server", server, "tool", tool, "approval", saved.ApprovalID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": saved.ID, "status": saved.Status})
}

// toolApprovalStatus is what the waiting gateway polls.
func (s *Server) toolApprovalStatus(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.runtimeFromGatewayToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Runtime 토큰을 확인할 수 없습니다.")
		return
	}
	item, err := s.store.ToolApprovalStatus(r.Context(), chi.URLParam(r, "id"), runtime.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval_not_found", "승인 요청을 찾을 수 없습니다.")
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": item.ID, "status": item.Status})
}
