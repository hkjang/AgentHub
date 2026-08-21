package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimeenv"
	"github.com/hkjang/AgentHub/internal/runtimespec"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
)

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.EnabledModelEndpoints(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) mcpBundles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.MCPBundles(r.Context(), true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) mcpServers(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.EnabledMCPServers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// CredentialConfigured is stored per shared credential; for per-user servers
	// it must reflect this caller's own keyring instead.
	status, statusErr := s.store.MCPCredentialStatus(r.Context(), u.ID)
	if statusErr != nil {
		writeStoreError(w, statusErr)
		return
	}
	for index := range items {
		if configured, tracked := status[items[index].ID]; tracked {
			items[index].CredentialConfigured = configured
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	var governance struct {
		TeamApprovalEnabled  bool `json:"teamApprovalEnabled"`
		HighRiskToolApproval bool `json:"highRiskToolApproval"`
	}
	var kubernetes struct {
		Enabled bool `json:"enabled"`
	}
	// The pause is here rather than only in the admin API because the people whose
	// work has stopped moving are not administrators: a queue that goes quiet with
	// no explanation is indistinguishable from a broken one.
	var operations store.OperationsSettings
	_ = s.store.Setting(r.Context(), "governance", &governance)
	_ = s.store.Setting(r.Context(), "kubernetes", &kubernetes)
	_ = s.store.Setting(r.Context(), store.OperationsSettingKey, &operations)
	writeJSON(w, http.StatusOK, map[string]any{
		"teamApprovalEnabled": governance.TeamApprovalEnabled, "highRiskToolApproval": governance.HighRiskToolApproval,
		"kubernetesEnabled": kubernetes.Enabled, "mcpProtocolVersion": currentMCPVersion,
		"executionPaused": operations.Paused, "executionPausedReason": operations.Reason,
	})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.Dashboard(r.Context(), u.ID, u.Role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Templates(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) runtimeProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.RuntimeProfiles(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Agents(r.Context(), u.ID, u.Role == "admin" && r.URL.Query().Get("scope") == "all")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.observeRuntimes(r.Context(), items) {
		items, _ = s.store.Agents(r.Context(), u.ID, u.Role == "admin" && r.URL.Query().Get("scope") == "all")
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input store.CreateAgentInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, 400, "invalid_name", "Agent 이름은 1~80자여야 합니다.")
		return
	}
	item, err := s.store.CreateAgent(r.Context(), u.ID, input)
	if errors.Is(err, store.ErrConflict) {
		// A reused name is the person's to fix, and the database's own words for it
		// are not something anybody can act on.
		writeStoreError(w, err)
		return
	}
	if err != nil {
		s.logger.Warn("create agent failed", "error", err, "user", u.ID)
		writeError(w, 400, "agent_create_failed", err.Error())
		return
	}
	// The first version is a version too: a rollback target has to exist from the
	// beginning, not from the first edit.
	s.snapshotAgent(r, item, "생성", u.ID)
	s.store.Audit(r.Context(), &u, "agent.create", "agent", item.ID, "success", clientIP(r), map[string]any{"runtimeType": item.RuntimeType})
	writeJSON(w, 201, item)
}

func (s *Server) spawnAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if refusal := policyRefusal(s.decide(r, u, policy.Request{
		Action: policy.ActionRuntimeStart, Agent: agent.Name, AgentID: agent.ID,
	})); refusal != "" {
		writeError(w, http.StatusForbidden, "policy_denied", refusal)
		return
	}
	var governance struct {
		TeamApprovalEnabled bool `json:"teamApprovalEnabled"`
	}
	_ = s.store.Setting(r.Context(), "governance", &governance)
	if governance.TeamApprovalEnabled && u.Role == "user" {
		approval, err := s.store.CreateApproval(r.Context(), u.ID, "agent", agent.ID, "spawn", "Agent Runtime 시작", map[string]any{"agentName": agent.Name})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		s.store.Audit(r.Context(), &u, "approval.request", "agent", agent.ID, "success", clientIP(r), nil)
		if approval.ReviewerID != nil {
			_ = s.store.CreateNotification(r.Context(), *approval.ReviewerID, "approval", "Agent 승인 요청", u.DisplayName+" 사용자가 "+agent.Name+" Runtime 실행 승인을 요청했습니다.", "/reviews")
		}
		writeJSON(w, 202, map[string]any{"approvalRequired": true, "approval": approval})
		return
	}
	rt, err := s.spawnNow(r, agent)
	if err != nil {
		if errors.Is(err, runtime.ErrNotConfigured) {
			writeJSON(w, 202, map[string]any{"runtime": rt, "warning": "Kubernetes 연결 전까지 Runtime은 pending 상태로 유지됩니다."})
			return
		}
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "runtime.spawn", "runtime", rt.ID, "success", clientIP(r), map[string]any{"agentId": agent.ID})
	_ = s.store.CreateNotification(r.Context(), u.ID, "runtime", "Agent 생성 요청 완료", agent.Name+" Runtime 생성 요청을 Kubernetes에 전달했습니다.", "/agents")
	writeJSON(w, 202, map[string]any{"runtime": rt})
}

func (s *Server) spawnNow(r *http.Request, agent store.Agent) (store.Runtime, error) {
	if existing, existingErr := s.store.LatestRuntimeForAgent(r.Context(), agent.ID); existingErr == nil {
		if existing.DesiredState == "running" {
			return existing, nil
		}
		spec, specErr := s.runtimeSpec(r, existing, agent)
		if specErr != nil {
			return existing, specErr
		}
		if err := s.store.CheckRuntimeQuota(r.Context(), agent.OwnerID, spec.Profile.ID); err != nil {
			return existing, err
		}
		startErr := s.spawner.Start(r.Context(), spec)
		updated, updateErr := s.store.UpdateRuntimeDesiredState(r.Context(), existing.ID, agent.OwnerID, "running", true)
		if updateErr != nil {
			return existing, updateErr
		}
		if startErr != nil {
			return updated, startErr
		}
		return updated, nil
	} else if !errors.Is(existingErr, store.ErrNotFound) {
		return store.Runtime{}, existingErr
	}
	profileID := "rp-basic"
	if agent.RuntimeProfileID != nil {
		profileID = *agent.RuntimeProfileID
	}
	if err := s.store.CheckRuntimeQuota(r.Context(), agent.OwnerID, profileID); err != nil {
		return store.Runtime{}, err
	}
	rt, err := s.store.CreateRuntime(r.Context(), agent, "pending")
	if err != nil {
		return rt, err
	}
	spec, err := s.runtimeSpec(r, rt, agent)
	if err != nil {
		return rt, err
	}
	if err := s.spawner.Spawn(r.Context(), spec); err != nil {
		return rt, err
	}
	return rt, nil
}

// observeRuntimes asks the cluster what each runtime is actually doing and writes
// it back, returning whether anything changed.
//
// It is shared because it used to live inside the agents listing alone, which
// meant the endpoint named after runtimes served whatever was last written by a
// visit to a different page: a Pod could be running for minutes while
// /api/v1/runtimes still said pending. An operator watching the Runtimes screen
// saw a runtime that never started.
func (s *Server) observeRuntimes(ctx context.Context, agents []store.Agent) bool {
	changed := false
	// One request for the whole namespace when the spawner can do it. Asking per
	// agent cost a settings read, a client construction and an API round trip each
	// — on the screen the console reloads most often, and for a list that is
	// already being fetched whole.
	var batch map[string]runtime.Status
	if reader, ok := s.spawner.(runtime.BatchStatus); ok {
		if all, err := reader.StatusAll(ctx); err == nil {
			batch = all
		} else {
			s.logger.Warn("runtime statuses could not be read together; asking one at a time", "error", err)
		}
	}
	for _, agent := range agents {
		if agent.Runtime == nil {
			continue
		}
		status, ok := statusFor(batch, agent)
		if !ok {
			observed, err := s.spawner.Status(ctx, runtime.Spec{Runtime: *agent.Runtime, Agent: agent})
			if err != nil {
				continue
			}
			status = observed
		}
		if err := s.store.UpdateRuntimeObserved(ctx, agent.Runtime.ID, status.Phase, status.PodName, status.NodeName, status.Endpoint, status.RestartCount, status.FailureReason); err == nil {
			changed = true
		}
	}
	return changed
}

// statusFor reads one agent's runtime out of the batch.
//
// A runtime the listing did not include is a runtime whose object is gone, which
// is the same thing the single-object read reports as Stopped — so it is answered
// here rather than sent back to the API server to be told the same thing.
func statusFor(batch map[string]runtime.Status, agent store.Agent) (runtime.Status, bool) {
	if batch == nil || agent.Runtime == nil {
		return runtime.Status{}, false
	}
	name := agent.Runtime.CRDName
	if name == "" {
		return runtime.Status{}, false
	}
	if status, ok := batch[name]; ok {
		return status, true
	}
	return runtime.Status{Phase: "Stopped"}, true
}

func (s *Server) runtimes(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agents, err := s.store.Agents(r.Context(), u.ID, u.Role == "admin" && r.URL.Query().Get("scope") == "all")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.observeRuntimes(r.Context(), agents) {
		agents, _ = s.store.Agents(r.Context(), u.ID, u.Role == "admin" && r.URL.Query().Get("scope") == "all")
	}
	items := []store.Runtime{}
	for _, a := range agents {
		if a.Runtime != nil {
			items = append(items, *a.Runtime)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) runtimeAction(state string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		current, err := s.store.RuntimeByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		agent, err := s.store.AgentByID(r.Context(), current.AgentID, u.ID, u.Role == "admin")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		spec, err := s.runtimeSpec(r, current, agent)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if state == "running" && current.DesiredState != "running" {
			// The quota belongs to whoever owns the runtime, not whoever pressed
			// start: an administrator acting on another user's agent must not spend
			// their own allowance nor let the owner exceed theirs.
			if err := s.store.CheckRuntimeQuota(r.Context(), agent.OwnerID, spec.Profile.ID); err != nil {
				writeError(w, http.StatusConflict, "quota_exceeded", err.Error())
				return
			}
		}
		if state == "running" {
			err = s.spawner.Start(r.Context(), spec)
		} else {
			err = s.spawner.Stop(r.Context(), spec)
		}
		if err != nil && !errors.Is(err, runtime.ErrNotConfigured) {
			writeStoreError(w, err)
			return
		}
		rt, err := s.store.UpdateRuntimeDesiredState(r.Context(), chi.URLParam(r, "id"), u.ID, state, u.Role == "admin")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		// A person acting on the runtime takes it over from the warm pool, which
		// must not then stop a workspace somebody is working in.
		s.releaseWarmClaim(r.Context(), rt)
		s.store.Audit(r.Context(), &u, "runtime."+state, "runtime", rt.ID, "success", clientIP(r), nil)
		writeJSON(w, 202, rt)
	}
}
func (s *Server) restartRuntime(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	current, err := s.store.RuntimeByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agent, err := s.store.AgentByID(r.Context(), current.AgentID, u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	spec, err := s.runtimeSpec(r, current, agent)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err = s.spawner.Restart(r.Context(), spec); err != nil && !errors.Is(err, runtime.ErrNotConfigured) {
		writeStoreError(w, err)
		return
	}
	rt, err := s.store.UpdateRuntimeDesiredState(r.Context(), chi.URLParam(r, "id"), u.ID, "running", u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.releaseWarmClaim(r.Context(), rt)
	s.store.Audit(r.Context(), &u, "runtime.restart", "runtime", rt.ID, "success", clientIP(r), nil)
	writeJSON(w, 202, rt)
}

func (s *Server) runtimeSpec(r *http.Request, rt store.Runtime, agent store.Agent) (runtime.Spec, error) {
	return s.runtimeSpecContext(r.Context(), rt, agent)
}

func (s *Server) runtimeSpecContext(ctx context.Context, rt store.Runtime, agent store.Agent) (runtime.Spec, error) {
	return runtimespec.New(s.store, s.logger).Build(ctx, rt, agent)
}

// loggingSettings is read for the one switch that governs who may read what a
// runtime printed.
//
// IncludeRuntimeLogs is a pointer because absent and false are different facts
// here. Every deployment that predates this reading had the key unset while the
// endpoint served logs, so treating unset as "off" would take the feature away
// from all of them in the name of honouring a switch they never touched.
type loggingSettings struct {
	IncludeRuntimeLogs *bool `json:"includeRuntimeLogs"`
}

// runtimeLogsAllowed reports whether an administrator has turned runtime log
// reading off. Only an explicit false counts.
func (s *Server) runtimeLogsAllowed(ctx context.Context) bool {
	var settings loggingSettings
	if err := s.store.Setting(ctx, "logging", &settings); err != nil {
		return true
	}
	return settings.IncludeRuntimeLogs == nil || *settings.IncludeRuntimeLogs
}

func (s *Server) runtimeLogs(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	rt, err := s.store.RuntimeByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agent, err := s.store.AgentByID(r.Context(), rt.AgentID, u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The switch in Logging settings meant nothing until now: it was saved,
	// validated and never read, so an administrator who turned runtime logs off
	// went on serving them to everybody who asked.
	if !s.runtimeLogsAllowed(r.Context()) {
		writeError(w, http.StatusForbidden, "runtime_logs_disabled",
			"관리자가 Runtime 로그 조회를 꺼 두었습니다. 시스템 설정 ▸ Logging에서 다시 켤 수 있습니다.")
		return
	}
	data, err := s.spawner.Logs(r.Context(), runtime.Spec{Runtime: rt, Agent: agent}, 200)
	if errors.Is(err, runtime.ErrNotConfigured) {
		writeJSON(w, 200, map[string]any{"source": "runtime", "lines": []string{}, "message": "Kubernetes 연결이 구성되지 않았습니다."})
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"source": "runtime", "content": string(data)})
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Workspaces(r.Context(), u.ID, u.Role == "admin" && r.URL.Query().Get("scope") == "all")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.Workspace
	if !decodeJSON(w, r, &item) {
		return
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || item.SizeGB < 0 || item.SizeGB > 2048 {
		writeError(w, 400, "invalid_workspace", "Workspace 이름과 크기를 확인해 주세요.")
		return
	}
	if item.SizeGB == 0 {
		item.SizeGB = 10
	}
	if err := s.store.CheckWorkspaceQuota(r.Context(), u.ID, item.SizeGB); err != nil {
		writeError(w, http.StatusConflict, "quota_exceeded", err.Error())
		return
	}
	if item.GitCredentialSecretID != nil && strings.TrimSpace(*item.GitCredentialSecretID) != "" {
		if item.Type != "git" {
			writeError(w, http.StatusBadRequest, "invalid_git_credential", "Git Credential은 Git Repository Workspace에만 연결할 수 있습니다.")
			return
		}
		if !slices.Contains([]string{"token", "ssh-key"}, item.GitCredentialKind) {
			writeError(w, http.StatusBadRequest, "invalid_git_credential_kind", "Git 인증 방식은 token 또는 ssh-key여야 합니다.")
			return
		}
		// Confirm the caller actually owns the secret before storing the reference.
		if _, _, err := s.store.RevealPersonalSecret(r.Context(), u.ID, *item.GitCredentialSecretID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "invalid_git_credential", "선택한 개인 Secret을 찾을 수 없습니다.")
				return
			}
			writeStoreError(w, err)
			return
		}
	} else {
		item.GitCredentialSecretID, item.GitCredentialKind, item.GitCredentialUsername = nil, "", ""
	}
	created, err := s.store.CreateWorkspace(r.Context(), u.ID, item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "workspace.create", "workspace", created.ID, "success", clientIP(r), nil)
	writeJSON(w, 201, created)
}

func (s *Server) workspaceSnapshots(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.WorkspaceSnapshots(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	changed := false
	for _, item := range items {
		if item.Status != "pending" && item.Status != "provisioning" {
			continue
		}
		status, size, statusErr := s.spawner.SnapshotStatus(r.Context(), runtime.SnapshotSpec{Name: item.StorageRef})
		if statusErr != nil || status == "" {
			continue
		}
		if s.store.UpdateWorkspaceSnapshotStatus(r.Context(), item.ID, status, size) == nil {
			changed = true
		}
	}
	if changed {
		items, _ = s.store.WorkspaceSnapshots(r.Context(), u.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_snapshot_name", "Snapshot 이름은 1~80자여야 합니다.")
		return
	}
	item, workspace, err := s.store.CreateWorkspaceSnapshot(r.Context(), u.ID, chi.URLParam(r, "id"), input.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	err = s.spawner.Snapshot(r.Context(), runtime.SnapshotSpec{Name: item.StorageRef, PVCName: workspace.PVCName})
	if errors.Is(err, runtime.ErrSnapshotsUnsupported) {
		_ = s.store.UpdateWorkspaceSnapshotStatus(r.Context(), item.ID, "unsupported", 0)
		s.logger.Warn("workspace snapshot unsupported", "workspace", workspace.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "snapshot_unsupported", "이 클러스터에는 CSI VolumeSnapshot이 설치되어 있지 않습니다. 관리자에게 snapshot.storage.k8s.io CRD와 Snapshot Controller 설치를 요청하세요.")
		return
	}
	if err != nil && !errors.Is(err, runtime.ErrNotConfigured) {
		_ = s.store.UpdateWorkspaceSnapshotStatus(r.Context(), item.ID, "failed", 0)
		writeStoreError(w, err)
		return
	}
	response := map[string]any{"snapshot": item}
	if errors.Is(err, runtime.ErrNotConfigured) {
		response["warning"] = "Kubernetes 연결 후 VolumeSnapshot이 생성됩니다."
	} else {
		item.Status = "provisioning"
		_ = s.store.UpdateWorkspaceSnapshotStatus(r.Context(), item.ID, item.Status, 0)
		response["snapshot"] = item
	}
	s.store.Audit(r.Context(), &u, "workspace.snapshot", "workspace", workspace.ID, "success", clientIP(r), map[string]any{"snapshotId": item.ID})
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) restoreWorkspaceSnapshot(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_workspace_name", "복원할 Workspace 이름은 1~80자여야 합니다.")
		return
	}
	_, source, err := s.store.WorkspaceSnapshotByID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.store.CheckWorkspaceQuota(r.Context(), u.ID, source.SizeGB); err != nil {
		writeError(w, http.StatusConflict, "quota_exceeded", err.Error())
		return
	}
	workspace, err := s.store.RestoreWorkspaceSnapshot(r.Context(), u.ID, chi.URLParam(r, "id"), input.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "workspace.restore", "workspace", workspace.ID, "success", clientIP(r), map[string]any{"snapshotId": chi.URLParam(r, "id")})
	writeJSON(w, http.StatusCreated, workspace)
}

func (s *Server) runtimeSessions(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.RuntimeSessions(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createRuntimeSession(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len(input.Title) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_session_title", "Session 제목은 1~120자여야 합니다.")
		return
	}
	runtimeItem, err := s.store.RuntimeByID(r.Context(), chi.URLParam(r, "id"), u.ID, false)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if runtimeItem.Status != "running" && runtimeItem.Status != "ready" {
		writeError(w, http.StatusConflict, "runtime_not_ready", "실행 중인 Runtime에서만 Session을 시작할 수 있습니다.")
		return
	}
	item, err := s.store.CreateRuntimeSession(r.Context(), u.ID, runtimeItem.ID, input.Title)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "runtime_session.create", "runtime-session", item.ID, "success", clientIP(r), map[string]any{"runtimeId": runtimeItem.ID})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) closeRuntimeSession(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.CloseRuntimeSession(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "runtime_session.close", "runtime-session", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

type workflowStep struct {
	ID        string   `json:"id"`
	AgentID   string   `json:"agentId"`
	DependsOn []string `json:"dependsOn"`
}

type workflowDefinition struct {
	Steps []workflowStep `json:"steps"`
}

func (s *Server) workflows(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Workflows(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveWorkflow(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.Workflow
	if !decodeJSON(w, r, &item) {
		return
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len(item.Name) > 80 || !slices.Contains([]string{"sequential", "parallel", "router", "supervisor", "reviewer", "consensus"}, item.Mode) {
		writeError(w, http.StatusBadRequest, "invalid_workflow", "Workflow 이름과 실행 방식을 확인해 주세요.")
		return
	}
	if item.MaxDepth == 0 {
		item.MaxDepth = 4
	}
	if item.MaxAgentCalls == 0 {
		item.MaxAgentCalls = 12
	}
	if item.MaxToolCalls == 0 {
		item.MaxToolCalls = 50
	}
	if item.MaxDurationSeconds == 0 {
		item.MaxDurationSeconds = 900
	}
	if item.MaxParallelAgents == 0 {
		item.MaxParallelAgents = 3
	}
	definition, err := s.checkWorkflow(r.Context(), u.ID, item)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_workflow_graph", err.Error())
		return
	}
	item.Definition, _ = json.Marshal(definition)
	saved, err := s.store.UpsertWorkflow(r.Context(), u.ID, item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "workflow.save", "workflow", saved.ID, "success", clientIP(r), map[string]any{"mode": saved.Mode, "steps": len(definition.Steps)})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) validateWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	item, err := s.store.WorkflowByID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	definition, err := s.checkWorkflow(r.Context(), u.ID, item)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_workflow_graph", err.Error())
		return
	}
	levels := workflowLevels(definition.Steps)
	s.store.Audit(r.Context(), &u, "workflow.validate", "workflow", item.ID, "success", clientIP(r), map[string]any{"levels": len(levels)})
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "levels": levels, "guardrails": map[string]any{"maxDepth": item.MaxDepth, "maxAgentCalls": item.MaxAgentCalls, "maxToolCalls": item.MaxToolCalls, "maxDurationSeconds": item.MaxDurationSeconds, "maxParallelAgents": item.MaxParallelAgents}})
}

func (s *Server) checkWorkflow(ctx context.Context, ownerID string, item store.Workflow) (workflowDefinition, error) {
	if item.MaxDepth < 1 || item.MaxDepth > 20 || item.MaxAgentCalls < 1 || item.MaxAgentCalls > 100 || item.MaxToolCalls < 1 || item.MaxToolCalls > 1000 || item.MaxDurationSeconds < 10 || item.MaxDurationSeconds > 86400 || item.MaxParallelAgents < 1 || item.MaxParallelAgents > 20 {
		return workflowDefinition{}, errors.New("Workflow 실행 보호 한도를 확인해 주세요")
	}
	var definition workflowDefinition
	if len(item.Definition) == 0 || json.Unmarshal(item.Definition, &definition) != nil || len(definition.Steps) == 0 {
		return definition, errors.New("Workflow에는 Agent Step이 하나 이상 필요합니다")
	}
	if len(definition.Steps) > item.MaxAgentCalls {
		return definition, errors.New("Step 수가 Max Agent Calls를 초과합니다")
	}
	ids := map[string]bool{}
	for _, step := range definition.Steps {
		if step.ID == "" || step.AgentID == "" || ids[step.ID] {
			return definition, errors.New("각 Step에는 고유 ID와 Agent가 필요합니다")
		}
		ids[step.ID] = true
		if _, err := s.store.AgentByID(ctx, step.AgentID, ownerID, false); err != nil {
			return definition, errors.New("Workflow에 접근할 수 없는 Agent가 포함되어 있습니다")
		}
	}
	for _, step := range definition.Steps {
		for _, dependency := range step.DependsOn {
			if !ids[dependency] || dependency == step.ID {
				return definition, errors.New("Step dependency가 올바르지 않습니다")
			}
		}
	}
	levels := workflowLevels(definition.Steps)
	if len(levels) == 0 {
		return definition, errors.New("Agent 호출 순환이 감지되었습니다")
	}
	if len(levels) > item.MaxDepth {
		return definition, errors.New("Workflow 깊이가 Max Depth를 초과합니다")
	}
	for _, level := range levels {
		if len(level) > item.MaxParallelAgents {
			return definition, errors.New("동시 실행 Step이 Max Parallel Agents를 초과합니다")
		}
	}
	return definition, nil
}

func workflowLevels(steps []workflowStep) [][]string {
	remaining := map[string]workflowStep{}
	completed := map[string]bool{}
	for _, step := range steps {
		remaining[step.ID] = step
	}
	levels := [][]string{}
	for len(remaining) > 0 {
		level := []string{}
		for id, step := range remaining {
			ready := true
			for _, dependency := range step.DependsOn {
				if !completed[dependency] {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, id)
			}
		}
		if len(level) == 0 {
			return nil
		}
		slices.Sort(level)
		levels = append(levels, level)
		for _, id := range level {
			delete(remaining, id)
			completed[id] = true
		}
	}
	return levels
}

type evaluationCase struct {
	Name              string `json:"name"`
	ExpectedRuntime   string `json:"expectedRuntime"`
	RequiresProfile   bool   `json:"requiresProfile"`
	RequiresModel     bool   `json:"requiresModel"`
	RequiresMCP       bool   `json:"requiresMcp"`
	RequiresWorkspace bool   `json:"requiresWorkspace"`
	RequiresRunning   bool   `json:"requiresRunning"`
	RequiresSecurity  bool   `json:"requiresSecurity"`
}

func (s *Server) evaluationTestSets(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.EvaluationTestSets(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) saveEvaluationTestSet(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.EvaluationTestSet
	if !decodeJSON(w, r, &item) {
		return
	}
	item.Name = strings.TrimSpace(item.Name)
	var cases []evaluationCase
	if item.Name == "" || len(item.Name) > 80 || item.PassThreshold < 1 || item.PassThreshold > 100 || json.Unmarshal(item.Cases, &cases) != nil || len(cases) == 0 || len(cases) > 50 {
		writeError(w, http.StatusBadRequest, "invalid_test_set", "Test Set 이름, 통과 기준과 1~50개의 검사 항목을 확인해 주세요.")
		return
	}
	for _, test := range cases {
		if strings.TrimSpace(test.Name) == "" || (test.ExpectedRuntime != "" && !runtimetype.IsSupported(test.ExpectedRuntime)) {
			writeError(w, http.StatusBadRequest, "invalid_test_case", "검사 항목 이름과 Runtime 조건을 확인해 주세요.")
			return
		}
	}
	saved, err := s.store.UpsertEvaluationTestSet(r.Context(), u.ID, item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "evaluation_test_set.save", "evaluation-test-set", saved.ID, "success", clientIP(r), map[string]any{"cases": len(cases)})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) agentEvaluations(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.AgentEvaluations(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Notifications(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	if err := s.store.ReadNotification(r.Context(), u.ID, chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) evaluateAgent(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		TestSetID string `json:"testSetId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, false)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	agentsWithRuntime, _ := s.store.Agents(r.Context(), u.ID, false)
	for _, candidate := range agentsWithRuntime {
		if candidate.ID == agent.ID {
			agent.Runtime = candidate.Runtime
			break
		}
	}
	testSet, err := s.store.EvaluationTestSetByID(r.Context(), u.ID, input.TestSetID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var cases []evaluationCase
	if err := json.Unmarshal(testSet.Cases, &cases); err != nil {
		writeStoreError(w, err)
		return
	}
	passed := 0
	results := make([]map[string]any, 0, len(cases))
	for _, test := range cases {
		failures := []string{}
		if test.ExpectedRuntime != "" && agent.RuntimeType != test.ExpectedRuntime {
			failures = append(failures, "runtime")
		}
		if test.RequiresProfile && agent.RuntimeProfileID == nil {
			failures = append(failures, "runtimeProfile")
		}
		if test.RequiresModel && agent.ModelEndpointID == nil {
			failures = append(failures, "model")
		}
		if test.RequiresMCP && agent.MCPBundleID == nil {
			failures = append(failures, "mcpBundle")
		}
		if test.RequiresWorkspace && agent.WorkspaceID == nil {
			failures = append(failures, "workspace")
		}
		if test.RequiresSecurity && (agent.SecurityProfileID == nil || agent.NetworkProfileID == nil) {
			failures = append(failures, "securityProfile")
		}
		if test.RequiresRunning && (agent.Runtime == nil || !slices.Contains([]string{"running", "ready"}, strings.ToLower(agent.Runtime.Status))) {
			failures = append(failures, "runtimeStatus")
		}
		ok := len(failures) == 0
		if ok {
			passed++
		}
		results = append(results, map[string]any{"name": test.Name, "passed": ok, "failures": failures})
	}
	score := passed * 100 / len(cases)
	status := "failed"
	if score >= testSet.PassThreshold {
		status = "passed"
	}
	metrics := map[string]any{"total": len(cases), "passed": passed, "failed": len(cases) - passed, "score": score, "threshold": testSet.PassThreshold, "evaluationType": "configuration-preflight"}
	item, err := s.store.CreateAgentEvaluation(r.Context(), u.ID, agent.ID, agent.Version, testSet.ID, status, score, metrics, map[string]any{"cases": results})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "agent.evaluate", "agent", agent.ID, status, clientIP(r), metrics)
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) personalSecrets(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.ListPersonalSecrets(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createPersonalSecret(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || input.Value == "" {
		writeError(w, 400, "invalid_secret", "이름과 비밀값을 모두 입력해 주세요.")
		return
	}
	if input.Kind == "" {
		input.Kind = "api_key"
	}
	item, err := s.store.PutPersonalSecret(r.Context(), u.ID, input.Name, input.Kind, input.Value)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "secret.create", "secret", item.ID, "success", clientIP(r), map[string]any{"kind": item.Kind})
	writeJSON(w, 201, item)
}
func (s *Server) deletePersonalSecret(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeletePersonalSecret(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "secret.delete", "secret", id, "success", clientIP(r), nil)
	w.WriteHeader(204)
}
func (s *Server) rotatePersonalKey(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	version, err := s.store.RotatePersonalKey(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "key.rotate", "user-keyring", u.ID, "success", clientIP(r), map[string]any{"version": version})
	writeJSON(w, 200, map[string]any{"version": version, "rotatedAt": time.Now()})
}

func (s *Server) apiKeys(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.ListAPIKeys(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Name == "" {
		writeError(w, 400, "invalid_name", "API 키 이름을 입력해 주세요.")
		return
	}
	if len(input.Scopes) == 0 {
		input.Scopes = []string{ScopeRead}
	}
	for _, scope := range input.Scopes {
		if !slices.Contains(APIKeyScopes, scope) {
			writeError(w, http.StatusBadRequest, "invalid_scope", "지원하지 않는 API Key scope입니다: "+scope)
			return
		}
	}
	item, token, err := s.store.CreateAPIKey(r.Context(), u.ID, input.Name, input.Scopes, input.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "api_key.create", "api-key", item.ID, "success", clientIP(r), map[string]any{"scopes": item.Scopes})
	writeJSON(w, 201, map[string]any{"apiKey": item, "token": token, "warning": "이 값은 다시 표시되지 않습니다."})
}
func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.RevokeAPIKey(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "api_key.revoke", "api-key", id, "success", clientIP(r), nil)
	w.WriteHeader(204)
}

func (s *Server) adminRuntimeProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.AllRuntimeProfiles(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) saveRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.RuntimeProfile
	if !decodeJSON(w, r, &item) {
		return
	}
	if item.Name == "" || item.CPUMillis < 100 || item.MemoryMB < 128 || item.StorageGB < 1 {
		writeError(w, 400, "invalid_profile", "Profile 이름과 자원 값을 확인해 주세요.")
		return
	}
	saved, err := s.store.UpsertRuntimeProfile(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "runtime_profile.save", "runtime-profile", saved.ID, "success", clientIP(r), nil)
	writeJSON(w, 200, saved)
}
func (s *Server) adminRuntimeImages(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.RuntimeImages(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) saveRuntimeImage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.RuntimeImage
	if !decodeJSON(w, r, &item) {
		return
	}
	if item.Name == "" || item.Image == "" || item.Version == "" || !runtimetype.IsSupported(item.RuntimeType) {
		writeError(w, 400, "invalid_image", "Runtime 유형, 이름, Image와 Version을 확인해 주세요.")
		return
	}
	saved, err := s.store.UpsertRuntimeImage(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "runtime_image.save", "runtime-image", saved.ID, "success", clientIP(r), nil)
	writeJSON(w, 200, saved)
}
func (s *Server) adminModels(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ModelEndpoints(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) saveModel(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		store.ModelEndpoint
		Secret *string `json:"secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Name == "" || input.Provider == "" || input.DefaultModel == "" {
		writeError(w, 400, "invalid_model", "Model 이름, Provider와 기본 Model을 확인해 주세요.")
		return
	}
	if parsed, err := url.Parse(input.BaseURL); err != nil || parsed.Host == "" {
		writeError(w, 400, "invalid_model_url", "Model Base URL을 확인해 주세요.")
		return
	}
	saved, err := s.store.UpsertModelEndpoint(r.Context(), input.ModelEndpoint, input.Secret)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "model_endpoint.save", "model-endpoint", saved.ID, "success", clientIP(r), nil)
	writeJSON(w, 200, saved)
}
func (s *Server) adminMCPServers(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.MCPServers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) saveMCPServer(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.MCPServer
	if !decodeJSON(w, r, &item) {
		return
	}
	if item.Name == "" || !slices.Contains([]string{"shared", "dedicated", "sidecar"}, item.Mode) {
		writeError(w, 400, "invalid_mcp_server", "MCP 이름과 Runtime Mode를 확인해 주세요.")
		return
	}
	if item.Mode == "shared" && item.Endpoint == "" {
		writeError(w, 400, "invalid_mcp_endpoint", "Shared MCP Endpoint를 입력해 주세요.")
		return
	}
	if item.Mode != "shared" && item.Image == "" {
		writeError(w, 400, "invalid_mcp_image", "Dedicated/Sidecar MCP Image를 입력해 주세요.")
		return
	}
	if item.AuthType == "" {
		item.AuthType = "none"
	}
	if !slices.Contains([]string{"none", "bearer", "header", "basic"}, item.AuthType) {
		writeError(w, 400, "invalid_mcp_auth", "MCP 인증 방식은 none, bearer, header, basic 중 하나여야 합니다.")
		return
	}
	if item.AuthType == "header" && strings.TrimSpace(item.AuthHeader) == "" {
		writeError(w, 400, "invalid_mcp_auth_header", "header 인증에는 헤더 이름이 필요합니다.")
		return
	}
	if item.Port < 0 || item.Port > 65535 {
		writeError(w, 400, "invalid_mcp_port", "MCP Port는 1~65535 범위여야 합니다.")
		return
	}
	saved, err := s.store.UpsertMCPServer(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "mcp_server.save", "mcp-server", saved.ID, "success", clientIP(r), nil)
	writeJSON(w, 200, saved)
}
func (s *Server) adminMCPBundles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.MCPBundles(r.Context(), false)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) saveMCPBundle(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var item store.MCPBundle
	if !decodeJSON(w, r, &item) {
		return
	}
	if item.Name == "" {
		writeError(w, 400, "invalid_mcp_bundle", "MCP Bundle 이름을 입력해 주세요.")
		return
	}
	saved, err := s.store.UpsertMCPBundle(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "mcp_bundle.save", "mcp-bundle", saved.ID, "success", clientIP(r), nil)
	writeJSON(w, 200, saved)
}

func (s *Server) adminPolicyProfiles(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := s.store.PolicyProfiles(r.Context(), kind)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func (s *Server) savePolicyProfile(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		var item store.PolicyProfile
		if !decodeJSON(w, r, &item) {
			return
		}
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" || len(item.Name) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_policy_profile", "Profile 이름은 1~80자여야 합니다.")
			return
		}
		if item.Spec == nil {
			item.Spec = map[string]any{}
		}
		if kind == "security" {
			runAsNonRoot, _ := item.Spec["runAsNonRoot"].(bool)
			privilegeEscalation, _ := item.Spec["allowPrivilegeEscalation"].(bool)
			serviceAccountToken, _ := item.Spec["automountServiceAccountToken"].(bool)
			seccomp, _ := item.Spec["seccompProfile"].(string)
			if !runAsNonRoot || privilegeEscalation || serviceAccountToken || (seccomp != "" && seccomp != "RuntimeDefault") {
				writeError(w, http.StatusBadRequest, "unsafe_security_profile", "Agent Runtime은 non-root, 권한 상승 차단, ServiceAccount Token 차단 및 RuntimeDefault seccomp를 반드시 유지해야 합니다.")
				return
			}
			// clusterRead is the one privilege a profile may grant. It is not a
			// relaxation of the four above: the token is projected with an audience
			// and an expiry rather than automounted, and it carries Kubernetes' own
			// view role, which cannot read Secrets.
			if clusterRead, _ := item.Spec["clusterRead"].(bool); clusterRead {
				s.store.Audit(r.Context(), &u, "security.profile.cluster_read", "security_profile", item.ID, "success", clientIP(r), map[string]any{"profile": item.Name})
			}
		}
		if kind == "network" {
			if values, ok := item.Spec["allowedDestinations"].([]any); ok {
				if len(values) > 64 {
					writeError(w, http.StatusBadRequest, "too_many_destinations", "허용 Destination은 최대 64개입니다.")
					return
				}
				for _, value := range values {
					raw, _ := value.(string)
					host, portText, splitErr := net.SplitHostPort(raw)
					port, portErr := strconv.Atoi(portText)
					if splitErr != nil || portErr != nil || port < 1 || port > 65535 {
						writeError(w, http.StatusBadRequest, "invalid_destination", "Destination은 CIDR:port 형식이어야 합니다. IPv6 CIDR은 대괄호로 감싸세요.")
						return
					}
					host = strings.Trim(host, "[]")
					if _, _, cidrErr := net.ParseCIDR(host); cidrErr != nil {
						writeError(w, http.StatusBadRequest, "invalid_destination", "Destination에는 DNS 이름 대신 CIDR을 입력해야 합니다.")
						return
					}
				}
			}
		}
		saved, err := s.store.UpsertPolicyProfile(r.Context(), kind, item)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		s.store.Audit(r.Context(), &u, kind+"_profile.save", kind+"-profile", saved.ID, "success", clientIP(r), nil)
		writeJSON(w, http.StatusOK, saved)
	}
}
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Users(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The list showed a role and a last login, which answers neither "what is
	// this account for" nor "is it still used". The activity beside each row is
	// the same aggregate the overview screen totals.
	from, to, windowErr := reportWindow(r, 30)
	if windowErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_window", windowErr.Error())
		return
	}
	activity, err := s.store.UserActivitySummary(r.Context(), from, to)
	if err != nil {
		s.logger.Warn("user activity is unreadable", "error", err)
		activity = map[string]store.UserActivity{}
	}
	writeJSON(w, 200, map[string]any{"items": items, "activity": activity, "from": from, "to": to})
}
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := userFromContext(r.Context())
	var input struct {
		Role      string  `json:"role"`
		Status    string  `json:"status"`
		ManagerID *string `json:"managerId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if chi.URLParam(r, "id") == actor.ID && input.Status == "disabled" {
		writeError(w, 400, "self_disable", "현재 로그인한 관리자 계정은 비활성화할 수 없습니다.")
		return
	}
	item, err := s.store.UpdateUserGovernance(r.Context(), chi.URLParam(r, "id"), input.Role, input.Status, input.ManagerID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &actor, "user.governance_update", "user", item.ID, "success", clientIP(r), map[string]any{"role": item.Role, "status": item.Status})
	writeJSON(w, 200, item)
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Settings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for key, raw := range items {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) == nil {
			if key == "authentication" {
				obj["clientSecretConfigured"] = s.secretConfigured(r, key)
			}
			items[key], _ = json.Marshal(obj)
		}
	}
	writeJSON(w, 200, items)
}
func (s *Server) secretConfigured(r *http.Request, key string) bool {
	value, err := s.store.SettingSecret(r.Context(), key)
	return err == nil && value != ""
}
func (s *Server) putAdminSetting(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	key := chi.URLParam(r, "key")
	allowed := map[string]bool{"general": true, "authentication": true, "kubernetes": true, "sessionGateway": true, "governance": true, "logging": true, "release": true, runtimeenv.SettingKey: true, telemetry.SettingKey: true}
	if !allowed[key] {
		writeError(w, 404, "setting_not_found", "지원하지 않는 설정입니다.")
		return
	}
	var input struct {
		Value  map[string]any `json:"value"`
		Secret *string        `json:"secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.validateSetting(r, key, input.Value, input.Secret); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_setting", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), key, input.Value, input.Secret, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	if key == "sessionGateway" {
		s.invalidateSessionGatewaySettings()
	}
	response := map[string]any{"saved": true}
	if key == runtimeenv.SettingKey {
		// Each runtime object carries a copy of these files and variables, so the
		// setting alone changes nothing that is already running. Pushing it here is
		// what makes "저장" mean what an administrator reads it to mean.
		sync, cancel := context.WithTimeout(r.Context(), runtimeEnvironmentSync)
		pushed := s.syncRuntimeEnvironment(sync)
		cancel()
		response["runtimeEnvironment"] = runtimeEnvironmentApplied(pushed)
		s.logger.Info("runtime environment pushed to existing runtimes",
			"applied", pushed.applied, "skipped", pushed.skipped, "failed", pushed.failed, "crdOutdated", pushed.pruned)
	}
	s.store.Audit(r.Context(), &u, "settings.update", "setting", key, "success", clientIP(r), map[string]any{"keys": mapKeys(input.Value)})
	writeJSON(w, 200, response)
}
func (s *Server) validateSetting(r *http.Request, key string, value map[string]any, secret *string) error {
	stringValue := func(name string) string { v, _ := value[name].(string); return strings.TrimSpace(v) }
	boolValue := func(name string) bool { v, _ := value[name].(bool); return v }
	number := func(name string) float64 { v, _ := value[name].(float64); return v }
	validHTTPS := func(raw string) bool {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return false
		}
		return parsed.Scheme == "https" || (parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))
	}
	switch key {
	case "general":
		if stringValue("serviceName") == "" {
			return errors.New("서비스 이름을 입력해 주세요")
		}
		if raw := stringValue("publicUrl"); raw != "" && !validHTTPS(raw) {
			return errors.New("Public URL은 HTTPS 주소여야 합니다 (localhost 제외)")
		}
	case "authentication":
		if boolValue("oidcEnabled") {
			if !validHTTPS(stringValue("issuerUrl")) {
				return errors.New("OIDC Issuer URL은 HTTPS 주소여야 합니다")
			}
			if stringValue("clientId") == "" {
				return errors.New("OIDC Client ID를 입력해 주세요")
			}
			if !s.secretConfigured(r, "authentication") && (secret == nil || strings.TrimSpace(*secret) == "") {
				return errors.New("OIDC Client Secret을 입력해 주세요")
			}
		}
		if !boolValue("localLoginEnabled") && !boolValue("oidcEnabled") {
			return errors.New("로컬 로그인과 OIDC를 동시에 비활성화할 수 없습니다")
		}
		if !boolValue("localLoginEnabled") {
			// The other way to lock everybody out: OIDC enabled is not OIDC
			// working. Turning off the only other door while the new one has never
			// opened leaves nobody able to get in — including whoever is saving
			// this. So the provider is asked here, on the way past.
			issuer, clientID := stringValue("issuerUrl"), stringValue("clientId")
			key := ""
			if secret != nil {
				key = strings.TrimSpace(*secret)
			}
			if key == "" {
				key, _ = s.store.SettingSecret(r.Context(), "authentication")
			}
			if result := checkOIDC(r.Context(), issuer, clientID, key); result.Verdict != "ok" {
				return errors.New("로컬 로그인을 끄기 전에 SSO가 실제로 동작해야 합니다 — " + result.Detail)
			}
		}
	case "kubernetes":
		if namespace := stringValue("namespace"); namespace == "" || len(namespace) > 63 {
			return errors.New("Runtime Namespace를 확인해 주세요")
		}
		if boolValue("enabled") && stringValue("mode") == "token" {
			if !validHTTPS(stringValue("apiServer")) {
				return errors.New("Kubernetes API Server는 HTTPS 주소여야 합니다")
			}
			if !s.secretConfigured(r, "kubernetes") && (secret == nil || strings.TrimSpace(*secret) == "") {
				return errors.New("Kubernetes Bearer Token을 입력해 주세요")
			}
		}
	case "sessionGateway":
		if boolValue("enabled") {
			hostname, _, err := splitHostPort(stringValue("baseDomain"))
			if err != nil {
				return errors.New("Runtime Base Domain을 확인해 주세요")
			}
			if hostname != "localhost" && (!strings.Contains(hostname, ".") || net.ParseIP(hostname) != nil) {
				return errors.New("Runtime Base Domain에는 wildcard DNS가 가능한 도메인이 필요합니다")
			}
			scheme := stringValue("scheme")
			if scheme != "https" && !(scheme == "http" && hostname == "localhost") {
				return errors.New("Session Gateway는 HTTPS를 사용해야 합니다 (localhost 제외)")
			}
			hours := number("sessionHours")
			if hours < 1 || hours > 24 {
				return errors.New("Runtime 세션 유효시간은 1~24시간이어야 합니다")
			}
		}
	case "governance":
		for _, name := range []string{"maxRuntimesPerUser", "maxCpuMillisPerUser", "maxMemoryMbPerUser", "maxStorageGbPerUser", "defaultIdleTimeoutSeconds",
			"maxRunningTasksPerUser", "tokenBudgetPerUser", "costBudgetPerUser"} {
			if number(name) < 0 {
				return errors.New("Quota와 Timeout은 0 이상이어야 합니다")
			}
		}
	case "logging":
		if !slices.Contains([]string{"debug", "info", "warn", "error"}, stringValue("level")) {
			return errors.New("지원하지 않는 로그 레벨입니다")
		}
		if number("retentionDays") < 1 || number("retentionDays") > 3650 {
			return errors.New("로그 보관 기간은 1~3650일이어야 합니다")
		}
	case "release":
		if boolValue("offlineMode") && boolValue("updateCheckEnabled") {
			return errors.New("Offline Mode에서는 외부 업데이트 확인을 사용할 수 없습니다")
		}
	case telemetry.SettingKey:
		// Checked here rather than at startup, where a bad endpoint would only show
		// up as a log line in a process nobody is watching.
		settings, err := decodeSetting[telemetry.Settings](value)
		if err != nil {
			return err
		}
		return settings.Validate()
	case runtimeenv.SettingKey:
		// Everything here ends up in a Pod spec, so it is checked against the same
		// rules the operator enforces rather than stored and quietly dropped later.
		settings, err := decodeRuntimeEnvironment(value)
		if err != nil {
			return err
		}
		return settings.Validate()
	}
	return nil
}

// decodeSetting re-reads a submitted settings document into its typed form.
func decodeSetting[T any](value map[string]any) (T, error) {
	var decoded T
	raw, err := json.Marshal(value)
	if err != nil {
		return decoded, err
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return decoded, errors.New("설정 형식을 확인해 주세요")
	}
	return decoded, nil
}

// decodeRuntimeEnvironment re-reads the submitted document into its typed form.
// Settings arrive as a free-form object, and this is the one setting whose shape
// the platform has to know exactly.
func decodeRuntimeEnvironment(value map[string]any) (runtimeenv.Settings, error) {
	var settings runtimeenv.Settings
	raw, err := json.Marshal(value)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, errors.New("Runtime 공통 설정 형식을 확인해 주세요: files와 variables만 사용할 수 있습니다")
	}
	return settings, nil
}

func mapKeys(value map[string]any) []string {
	result := make([]string, 0, len(value))
	for k := range value {
		result = append(result, k)
	}
	return result
}
func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	level := slog.LevelInfo
	if raw := r.URL.Query().Get("level"); raw != "" {
		_ = level.UnmarshalText([]byte(raw))
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, 200, map[string]any{"items": s.logs.Entries(level, r.URL.Query().Get("q"), limit)})
}
func (s *Server) adminApprovals(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Approvals(r.Context(), u.ID, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) reviewerApprovals(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.Approvals(r.Context(), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// approvalEventPayload says which agent the decision was about.
//
// A trigger filters events by payload containment, and the guide offers
// `{"agentId":"…"}` as the way to watch one agent — so an event that never
// carries an agent id can never match such a filter. This one carried the
// decision, the action and the reason: enough to read, and nothing to filter on.
// A subscription to 승인 처리 for one agent was accepted, saved, and silent.
//
// The approval already knows. Its own payload carries the agent, task and run
// that asked for it, and a runtime spawn approval is about the agent named in its
// resource id. Both are put back into the event.
func approvalEventPayload(item store.Approval, decision string) json.RawMessage {
	fields := map[string]any{
		"decision": decision, "action": item.Action, "reason": item.Reason,
		"approvalId": item.ID, "resourceType": item.ResourceType, "resourceId": item.ResourceID,
	}
	var asked map[string]any
	if err := json.Unmarshal(item.Payload, &asked); err == nil {
		for _, key := range []string{"agentId", "taskId", "runId"} {
			if value, ok := asked[key]; ok {
				fields[key] = value
			}
		}
	}
	if item.ResourceType == "agent" {
		fields["agentId"] = item.ResourceID
	}
	return eventPayload(fields)
}

func (s *Server) decideApproval(decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFromContext(r.Context())
		item, err := s.store.DecideApproval(r.Context(), chi.URLParam(r, "id"), u.ID, decision, u.Role == "admin")
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if decision == "approved" && item.ResourceType == "agent" && item.Action == "spawn" {
			agent, agentErr := s.store.AgentByID(r.Context(), item.ResourceID, item.RequesterID, true)
			if agentErr == nil {
				_, spawnErr := s.spawnNow(r, agent)
				if spawnErr != nil && !errors.Is(spawnErr, runtime.ErrNotConfigured) {
					s.logger.Error("spawn approved runtime", "approval", item.ID, "error", spawnErr)
					writeStoreError(w, spawnErr)
					return
				}
			}
		}
		// An agent parked at an approval gate resumes — or stops — on this decision.
		resourceURL := "/agents"
		if item.ResourceType == "task" {
			resourceURL = "/tasks"
			if decision == "approved" {
				if _, resumeErr := s.store.ResumeApprovedTask(r.Context(), item.ID); resumeErr != nil && !errors.Is(resumeErr, store.ErrNotFound) {
					writeStoreError(w, resumeErr)
					return
				}
			} else if _, failErr := s.store.FailRejectedTask(r.Context(), item.ID, "승인이 거절되었습니다: "+item.Reason); failErr != nil && !errors.Is(failErr, store.ErrNotFound) {
				writeStoreError(w, failErr)
				return
			}
		}
		s.store.Audit(r.Context(), &u, "approval."+decision, item.ResourceType, item.ResourceID, "success", clientIP(r), nil)
		s.publishEvent(r.Context(), store.PlatformEvent{
			Type: store.EventApprovalDecided, OwnerID: item.RequesterID,
			SubjectType: item.ResourceType, SubjectID: item.ResourceID,
			Payload: approvalEventPayload(item, decision),
		})
		_ = s.store.CreateNotification(r.Context(), item.RequesterID, "approval", "승인 요청 "+decision, item.Reason, resourceURL)
		writeJSON(w, 200, item)
	}
}
