package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// writeDeleteError adds the "still referenced" case to the standard mapping.
// Platform resources are shared, so a delete that would strand a live Agent
// definition is a conflict the user can act on, not a server fault.
func writeDeleteError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, store.ErrInUse) {
		writeError(w, http.StatusConflict, "resource_in_use", message)
		return
	}
	writeStoreError(w, err)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input store.CreateAgentInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Agent 이름은 1~80자여야 합니다.")
		return
	}
	// `agent.update` is one of the actions the policy screen offers, and it was
	// evaluated nowhere: an administrator could write "이 역할은 에이전트를 수정할 수
	// 없다", see it saved, and watch that role go on editing agents. A rule in the
	// engine that no code asks about is worse than no rule, because the screen
	// says it is in force.
	if refusal := policyRefusal(s.decide(r, u, policy.Request{
		Action: policy.ActionAgentUpdate, Agent: input.Name, AgentID: chi.URLParam(r, "id"),
	})); refusal != "" {
		writeError(w, http.StatusForbidden, "policy_denied", refusal)
		return
	}
	item, err := s.store.UpdateAgent(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin", input)
	if errors.Is(err, store.ErrConflict) {
		writeStoreError(w, err)
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, "agent_update_failed", err.Error())
		return
	}
	s.snapshotAgent(r, item, "수정", u.ID)
	s.store.Audit(r.Context(), &u, "agent.update", "agent", item.ID, "success", clientIP(r), map[string]any{"version": item.Version})
	// The Pod still runs the previous definition until it is recreated, so tell
	// the caller rather than letting the change look applied when it is not.
	response := map[string]any{"agent": item}
	if existing, runtimeErr := s.store.LatestRuntimeForAgent(r.Context(), item.ID); runtimeErr == nil && existing.DesiredState == "running" {
		response["warning"] = "실행 중인 Runtime에는 재시작 후 반영됩니다."
	}
	writeJSON(w, http.StatusOK, response)
}

// deleteAgent tears down the Kubernetes resources before dropping the row.
// agent_runtimes cascades from the definition, so deleting the row first would
// lose the CRD name and orphan the StatefulSet in the cluster.
func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	agent, err := s.store.AgentByID(r.Context(), id, u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if existing, runtimeErr := s.store.LatestRuntimeForAgent(r.Context(), agent.ID); runtimeErr == nil {
		spec, specErr := s.runtimeSpec(r, existing, agent)
		if specErr != nil {
			writeStoreError(w, specErr)
			return
		}
		if deleteErr := s.spawner.Delete(r.Context(), spec); deleteErr != nil && !errors.Is(deleteErr, runtime.ErrNotConfigured) {
			s.logger.Error("runtime delete failed", "agent", agent.ID, "error", deleteErr)
			writeError(w, http.StatusBadGateway, "runtime_delete_failed", "Runtime을 정리하지 못했습니다: "+deleteErr.Error())
			return
		}
	} else if !errors.Is(runtimeErr, store.ErrNotFound) {
		writeStoreError(w, runtimeErr)
		return
	}
	if err := s.store.DeleteAgent(r.Context(), id, u.ID, u.Role == "admin"); err != nil {
		writeDeleteError(w, err, "이 Agent를 참조하는 리소스가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "agent.delete", "agent", id, "success", clientIP(r), map[string]any{"name": agent.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_workspace_name", "Workspace 이름은 1~80자여야 합니다.")
		return
	}
	item, err := s.store.UpdateWorkspace(r.Context(), chi.URLParam(r, "id"), u.ID, input.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "workspace.update", "workspace", item.ID, "success", clientIP(r), nil)
	writeJSON(w, http.StatusOK, item)
}

// deleteWorkspace refuses while agents are still bound, and deliberately leaves
// the PVC in place: the volume holds the user's files and is recoverable, while
// an accidental delete of it is not.
func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	bound, err := s.store.WorkspaceAgentRefs(r.Context(), id, u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(bound) > 0 {
		writeError(w, http.StatusConflict, "workspace_in_use", "다음 Agent가 이 Workspace를 사용 중입니다: "+strings.Join(bound, ", "))
		return
	}
	item, err := s.store.DeleteWorkspace(r.Context(), id, u.ID)
	if err != nil {
		writeDeleteError(w, err, "이 Workspace를 참조하는 리소스가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "workspace.delete", "workspace", id, "success", clientIP(r), map[string]any{"pvcName": item.PVCName})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": item, "notice": "영속 볼륨(PVC)은 보존됩니다. 저장소를 회수하려면 관리자가 직접 삭제해야 합니다."})
}

func (s *Server) deleteWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.DeleteWorkspaceSnapshot(r.Context(), chi.URLParam(r, "id"), u.ID)
	if err != nil {
		writeDeleteError(w, err, "이 Snapshot을 참조하는 Workspace가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "workspace.snapshot.delete", "snapshot", item.ID, "success", clientIP(r), map[string]any{"storageRef": item.StorageRef})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteWorkflow(r.Context(), id, u.ID); err != nil {
		writeDeleteError(w, err, "이 Workflow를 참조하는 리소스가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "workflow.delete", "workflow", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteEvaluationTestSet(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteEvaluationTestSet(r.Context(), id, u.ID); err != nil {
		writeDeleteError(w, err, "이 Test Set을 사용한 Evaluation 결과가 있어 삭제할 수 없습니다.")
		return
	}
	s.store.Audit(r.Context(), &u, "evaluation.test_set.delete", "evaluation", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// deleteAdminResource removes a shared platform resource. MCP bundle membership
// is a text[] rather than a foreign key, so that reference is checked explicitly
// instead of relying on the database to reject the delete.
func (s *Server) deleteAdminResource(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		id := chi.URLParam(r, "id")
		if kind == "mcp-servers" {
			bundles, err := s.store.MCPServerBundleRefs(r.Context(), id)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			if len(bundles) > 0 {
				writeError(w, http.StatusConflict, "resource_in_use", "다음 MCP Bundle이 이 서버를 포함하고 있습니다: "+strings.Join(bundles, ", "))
				return
			}
		}
		if err := s.store.DeleteAdminResource(r.Context(), kind, id); err != nil {
			writeDeleteError(w, err, "이 리소스를 사용 중인 Agent가 있어 삭제할 수 없습니다. 먼저 Agent 설정을 변경해 주세요.")
			return
		}
		s.store.Audit(r.Context(), &u, "admin."+kind+".delete", kind, id, "success", clientIP(r), nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

// putMCPCredential stores the credential a runtime presents to an MCP server.
// Administrators manage the shared platform credential; every user manages their
// own for servers configured with per-user credentials.
func (s *Server) putMCPCredential(shared bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		serverID := chi.URLParam(r, "id")
		var input struct {
			Value string `json:"value"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if strings.TrimSpace(input.Value) == "" {
			writeError(w, http.StatusBadRequest, "invalid_credential", "MCP 자격증명 값을 입력해 주세요.")
			return
		}
		server, err := s.store.MCPServerByID(r.Context(), serverID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if server.AuthType == "" || server.AuthType == "none" {
			writeError(w, http.StatusConflict, "mcp_auth_disabled", "이 MCP 서버는 인증을 사용하지 않도록 설정되어 있습니다.")
			return
		}
		owner := u.ID
		if shared {
			if server.PerUserCredential {
				writeError(w, http.StatusConflict, "mcp_per_user_credential", "이 MCP 서버는 사용자별 자격증명을 사용합니다. 각 사용자가 직접 등록해야 합니다.")
				return
			}
			owner = ""
		} else if !server.PerUserCredential {
			writeError(w, http.StatusConflict, "mcp_shared_credential", "이 MCP 서버는 공용 자격증명을 사용합니다. 관리자에게 요청해 주세요.")
			return
		}
		if err := s.store.PutMCPCredential(r.Context(), serverID, owner, input.Value); err != nil {
			writeStoreError(w, err)
			return
		}
		// The value itself is never logged or echoed back.
		s.store.Audit(r.Context(), &u, "mcp.credential.update", "mcp-server", serverID, "success", clientIP(r), map[string]any{"scope": credentialScope(shared)})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) deleteMCPCredential(shared bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		serverID := chi.URLParam(r, "id")
		owner := u.ID
		if shared {
			owner = ""
		}
		if err := s.store.DeleteMCPCredential(r.Context(), serverID, owner); err != nil {
			writeStoreError(w, err)
			return
		}
		s.store.Audit(r.Context(), &u, "mcp.credential.delete", "mcp-server", serverID, "success", clientIP(r), map[string]any{"scope": credentialScope(shared)})
		w.WriteHeader(http.StatusNoContent)
	}
}

func credentialScope(shared bool) string {
	if shared {
		return store.MCPCredentialShared
	}
	return store.MCPCredentialPerUser
}
