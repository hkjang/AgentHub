package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/runtimecfg"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// Runtime settings: what every runtime of one type starts with, and proof that it
// did.
//
// The platform generates each runtime's own configuration. Anything else a site
// needed — its locale, its time zone, whatever option that product exposes — had
// nowhere to go, because a second copy of the same file would fight the generated
// one. These overlays merge into it, are re-applied on every start and restart, and
// come back reported from inside the Pod: an operator should not have to open a
// runtime and read a file to find out whether their change took.

func (s *Server) runtimeSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.runtimeSettingsDocument(r)
	if settings.Profiles == nil {
		settings.Profiles = []runtimecfg.Profile{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":    settings,
		"suggestions": runtimecfg.Suggestions(strings.TrimSpace(r.URL.Query().Get("runtimeType"))),
		"runtimes":    runtimetype.Descriptors(),
		"targets":     []string{runtimecfg.TargetConfig, runtimecfg.TargetEnv},
	})
}

func (s *Server) runtimeSettingsDocument(r *http.Request) runtimecfg.Settings {
	var settings runtimecfg.Settings
	if err := s.store.Setting(r.Context(), runtimecfg.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("runtime settings are unreadable", "error", err)
	}
	return settings
}

func (s *Server) putRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var settings runtimecfg.Settings
	if !decodeJSON(w, r, &settings) {
		return
	}
	if err := settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_runtime_settings", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), runtimecfg.SettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	types := make([]string, 0, len(settings.Profiles))
	for _, profile := range settings.Profiles {
		types = append(types, profile.RuntimeType)
	}
	s.store.Audit(r.Context(), &u, "runtime_settings.update", "runtime", "", "success", clientIP(r),
		map[string]any{"runtimeTypes": types})
	// A saved overlay reaches a running Pod the same way the runtime environment
	// does: the object is rewritten and the Pod rolls if its content changed.
	applied := s.syncRuntimeEnvironment(r.Context())
	s.logger.Warn("runtime settings changed", "by", u.Username, "profiles", len(settings.Profiles),
		"applied", applied.applied, "failed", applied.failed)
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "profiles": len(settings.Profiles),
		"runtimes": map[string]any{"applied": applied.applied, "failed": applied.failed, "skipped": applied.skipped},
		"message":  runtimeSettingsSaved(applied),
	})
}

func runtimeSettingsSaved(applied syncResult) string {
	switch {
	case applied.pruned:
		return "저장했지만 클러스터의 AgentRuntime CRD가 오래되어 설정이 Pod에 전달되지 않습니다. deploy/kubernetes/crd.yaml을 다시 적용해 주세요."
	case applied.applied > 0:
		return "저장하고 실행 중인 런타임 " + plural(applied.applied) + "에 적용했습니다. 설정이 바뀐 Pod는 재시작되며, 재시작 후 주입 상태가 아래에 보고됩니다."
	case applied.failed > 0:
		return "저장했지만 런타임 " + plural(applied.failed) + "에 전달하지 못했습니다. 로그를 확인해 주세요."
	default:
		return "저장했습니다. 새로 시작하는 런타임부터 적용되고, 시작 시 주입 결과가 보고됩니다."
	}
}

// runtimeConfigStatus reports, per runtime, whether what is running matches what
// the platform would send now.
//
// This is the whole point of the feature: "설정을 저장했다" and "런타임이 그 설정으로
// 돌고 있다" are different claims, and only the second one matters.
func (s *Server) runtimeConfigStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	settings := s.runtimeSettingsDocument(r)
	reports, err := s.store.RuntimeConfigReports(r.Context(), 200)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	byRuntime := make(map[string]store.RuntimeConfigReport, len(reports))
	for _, report := range reports {
		byRuntime[report.RuntimeID] = report
	}
	agents, err := s.store.Agents(r.Context(), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		if agent.Runtime == nil {
			continue
		}
		expected := settings.For(agent.RuntimeType).Fingerprint()
		if settings.For(agent.RuntimeType).Empty() {
			expected = ""
		}
		report, reported := byRuntime[agent.Runtime.ID]
		items = append(items, map[string]any{
			"agentId": agent.ID, "agentName": agent.Name, "runtimeId": agent.Runtime.ID,
			"runtimeType": agent.RuntimeType, "runtimeStatus": agent.Runtime.Status,
			"expectedFingerprint": expected,
			"reported":            reported,
			"report":              reportOrNil(report, reported),
			"state":               injectionState(expected, report, reported, agent.Runtime.Status),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func reportOrNil(report store.RuntimeConfigReport, reported bool) any {
	if !reported {
		return nil
	}
	return report
}

// injectionState names what an operator is looking at.
//
// The states are deliberately blunt. "설정 없음" is not a problem; "확인 안 됨" is not
// a failure either — a Pod that has not restarted since the setting changed has
// simply not applied it yet, and saying "failed" would send somebody debugging
// something that is working as designed.
//
// A partial report is its own state. A Pod that wrote the file but is missing a
// declared variable is neither applied nor failed, and the difference is exactly
// what the person fixing it needs to know.
func injectionState(expected string, report store.RuntimeConfigReport, reported bool, runtimeStatus string) string {
	switch {
	case expected == "":
		return "none"
	case !reported:
		if runtimeStatus == "stopped" || runtimeStatus == "" {
			return "pending_start"
		}
		return "unverified"
	case report.Status == "incomplete":
		if report.Fingerprint != expected {
			return "stale"
		}
		return "partial"
	case report.Status != "applied":
		return "failed"
	case report.Fingerprint != expected:
		return "stale"
	default:
		return "applied"
	}
}

// reportRuntimeConfig receives the report an initialiser sends from inside a Pod.
//
// It authenticates with the runtime's own token, the same way the in-Pod gateway
// asks for tool approvals: the Pod is the only thing that can say what is on its
// disk, and it has no other credential.
func (s *Server) reportRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.runtimeFromGatewayToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Runtime 토큰을 확인할 수 없습니다.")
		return
	}
	var input struct {
		RuntimeID   string   `json:"runtimeId"`
		RuntimeType string   `json:"runtimeType"`
		Fingerprint string   `json:"fingerprint"`
		Status      string   `json:"status"`
		Detail      string   `json:"detail"`
		File        string   `json:"file"`
		Keys        []string `json:"keys"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// The body's runtime id is a cross-check, never the authority: the token is.
	if input.RuntimeID != "" && input.RuntimeID != runtime.ID {
		writeError(w, http.StatusForbidden, "runtime_mismatch", "다른 Runtime의 설정을 보고할 수 없습니다.")
		return
	}
	status := strings.TrimSpace(input.Status)
	// "incomplete" is the report a Pod sends when the overlay's file landed but a
	// declared environment variable did not reach the container. It is a partial
	// application, and rounding it to "unreadable" would hide which half failed.
	if status != "applied" && status != "missing" && status != "unreadable" && status != "incomplete" {
		status = "unreadable"
	}
	keys := input.Keys
	if len(keys) > 200 {
		keys = keys[:200]
	}
	if keys == nil {
		keys = []string{}
	}
	report := store.RuntimeConfigReport{
		RuntimeID: runtime.ID, AgentID: runtime.AgentID, RuntimeType: input.RuntimeType,
		Fingerprint: strings.TrimSpace(input.Fingerprint), Status: status,
		Detail: truncate(input.Detail, 400), File: truncate(input.File, 300), Keys: keys,
	}
	if err := s.store.SaveRuntimeConfigReport(r.Context(), report); err != nil {
		writeStoreError(w, err)
		return
	}
	s.logger.Info("runtime reported its configuration", "runtime", runtime.ID, "agent", runtime.AgentID,
		"status", status, "fingerprint", report.Fingerprint, "keys", len(keys))
	writeJSON(w, http.StatusAccepted, map[string]any{"recorded": true})
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// runtimeConfigReport is one runtime's report, for the agent detail drawer.
func (s *Server) runtimeConfigReport(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	rt, err := s.store.RuntimeByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	settings := s.runtimeSettingsDocument(r)
	agent, err := s.store.AgentByID(r.Context(), rt.AgentID, u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	profile := settings.For(agent.RuntimeType)
	expected := profile.Fingerprint()
	if profile.Empty() {
		expected = ""
	}
	report, reportErr := s.store.RuntimeConfigReportByRuntime(r.Context(), rt.ID)
	reported := reportErr == nil
	if reportErr != nil && !errors.Is(reportErr, store.ErrNotFound) {
		writeStoreError(w, reportErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtimeId": rt.ID, "runtimeType": agent.RuntimeType, "runtimeStatus": rt.Status,
		"expectedFingerprint": expected, "expectedKeys": profile.Keys(),
		"reported": reported, "report": reportOrNil(report, reported),
		"state": injectionState(expected, report, reported, rt.Status),
	})
}
