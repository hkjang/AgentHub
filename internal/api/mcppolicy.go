package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// maxPolicyTools bounds one policy. A list longer than this is a sign the
// operator meant the other mode.
const maxPolicyTools = 200

// agentMCPPolicies lists an agent's tool policies alongside the servers it is
// actually bound to, so the console can offer the servers rather than ask the
// operator to know their identifiers.
func (s *Server) agentMCPPolicies(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	policies, err := s.store.MCPToolPolicies(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	servers := []store.MCPServer{}
	if agent.MCPBundleID != nil {
		bound, bundleErr := s.store.MCPServersForBundle(r.Context(), *agent.MCPBundleID)
		if bundleErr != nil {
			writeStoreError(w, bundleErr)
			return
		}
		servers = bound
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": policies, "servers": servers})
}

func (s *Server) saveAgentMCPPolicy(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input store.MCPToolPolicy
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Mode != "allow" && input.Mode != "deny" {
		writeError(w, http.StatusBadRequest, "invalid_policy_mode", "도구 정책은 allow 또는 deny여야 합니다.")
		return
	}
	// The server has to be one this agent is actually bound to, or the policy
	// would sit there looking effective while applying to nothing.
	if agent.MCPBundleID == nil {
		writeError(w, http.StatusBadRequest, "no_mcp_bundle", "이 Agent에는 MCP 번들이 연결되어 있지 않습니다.")
		return
	}
	servers, err := s.store.MCPServersForBundle(r.Context(), *agent.MCPBundleID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	bound := false
	for _, server := range servers {
		if server.ID == input.ServerID {
			bound = true
			break
		}
	}
	if !bound {
		writeError(w, http.StatusBadRequest, "server_not_bound", "이 Agent에 연결되지 않은 MCP 서버입니다.")
		return
	}

	tools := make([]string, 0, len(input.Tools))
	for _, tool := range input.Tools {
		tool = strings.TrimSpace(tool)
		if tool != "" {
			tools = append(tools, tool)
		}
	}
	if len(tools) > maxPolicyTools {
		writeError(w, http.StatusBadRequest, "too_many_tools", "도구 목록이 너무 깁니다.")
		return
	}
	input.AgentID, input.Tools = agent.ID, tools
	saved, err := s.store.PutMCPToolPolicy(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "policy_save_failed", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "mcp.policy.save", "agent", agent.ID, "success", clientIP(r), map[string]any{"serverId": saved.ServerID, "mode": saved.Mode, "tools": len(saved.Tools)})
	// The Pod runs the previous policy until it is recreated, and a policy that
	// looks applied but is not would be worse than no policy at all.
	response := map[string]any{"policy": saved}
	if existing, runtimeErr := s.store.LatestRuntimeForAgent(r.Context(), agent.ID); runtimeErr == nil && existing.DesiredState == "running" {
		response["warning"] = "실행 중인 Runtime에는 재시작 후 반영됩니다."
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deleteAgentMCPPolicy(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	if err := s.store.DeleteMCPToolPolicy(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, "policy_delete_failed", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "mcp.policy.delete", "mcp-tool-policy", chi.URLParam(r, "id"), "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}
