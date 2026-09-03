package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/store"
)

// Configuring and trying out the content scanner.
//
// An agent is a program that reads whatever it is pointed at and then sends it
// somewhere else. On an offline site that is exactly the risk: the data never
// had to leave the building until an agent summarised it into a prompt. What is
// scanned, and what happens when something is found, is decided here.

// adminDLP returns the settings alongside what the platform can detect, so the
// console offers the classes this build actually has rather than a hard-coded
// list that drifts.
func (s *Server) adminDLP(w http.ResponseWriter, r *http.Request) {
	settings := s.dlpSettings(r)
	detectors := make([]map[string]any, 0)
	for _, detector := range dlp.Detectors() {
		detectors = append(detectors, map[string]any{
			"class": detector.Class, "label": detector.Label, "description": detector.Description,
			"action": settings.Action(detector.Class),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings, "detectors": detectors, "actions": dlp.Actions,
		"defaultMaxBytes": dlp.DefaultMaxBytes,
	})
}

func (s *Server) dlpSettings(r *http.Request) dlp.Settings {
	var settings dlp.Settings
	if err := s.store.Setting(r.Context(), dlp.SettingKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("DLP settings are unreadable", "error", err)
	}
	return settings
}

func (s *Server) putDLP(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var settings dlp.Settings
	if !decodeJSON(w, r, &settings) {
		return
	}
	if err := settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_dlp", err.Error())
		return
	}
	if err := s.store.PutSetting(r.Context(), dlp.SettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "dlp.update", "dlp", "", "success", clientIP(r),
		map[string]any{"enabled": settings.Enabled, "classes": settings.Classes, "scanResponses": settings.ScanResponses})
	s.logger.Warn("content scanner settings changed", "by", u.Username, "enabled", settings.Enabled)
	// Tool-call scanning happens inside each Pod, so the settings travel with the
	// runtime the same way the tool policy does.
	applied := s.syncRuntimeEnvironment(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "settings": settings,
		"runtimes": map[string]any{"applied": applied.applied, "failed": applied.failed, "skipped": applied.skipped},
		"message":  dlpSaved(settings, applied),
	})
}

func dlpSaved(settings dlp.Settings, applied syncResult) string {
	if !settings.Enabled {
		return "내용 검사를 껐습니다. 저장된 규칙은 유지되며, 다시 켜면 그대로 적용됩니다."
	}
	// On, and inspecting nothing. A class that is not listed is not scanned — that
	// is the rule which keeps a new detector from blocking anybody's traffic
	// unasked — and with no class listed at all it means the scanner reports
	// itself as enabled while every payload goes through untouched. That is the
	// one state where a security control is worse than being off, because the
	// screen says it is on.
	if len(scannedClasses(settings)) == 0 {
		return "내용 검사를 켰지만 **검사할 데이터 종류를 하나도 고르지 않았습니다** — 지금 상태로는 아무것도 검사하지 않습니다. 아래에서 종류를 고르고 각각 어떻게 처리할지 정해 주세요."
	}
	switch {
	case applied.pruned:
		return "저장했지만 클러스터의 AgentRuntime CRD가 오래되어 도구 호출 검사 설정이 Pod에 전달되지 않습니다. deploy/kubernetes/crd.yaml을 다시 적용해 주세요."
	case applied.applied > 0:
		return "저장하고 실행 중인 런타임 " + plural(applied.applied) + "에 적용했습니다. 모델 호출은 즉시, 도구 호출은 Pod 재시작 후 적용됩니다."
	default:
		return "저장했습니다. 모델 호출에는 즉시 적용되고, 도구 호출 검사는 새로 시작하는 런타임부터 적용됩니다."
	}
}

// scanSample runs the scanner over text an administrator pastes in.
//
// "Does it catch this" is the first question anybody asks about a DLP tool, and
// the only honest way to answer it is to let them try. The sample is scanned and
// discarded: it is never stored, and the response carries the same masked
// findings the audit trail would.
func (s *Server) scanSample(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text string `json:"text"`
		// Settings lets the console try an unsaved change, the same way the policy
		// simulator does.
		Settings *dlp.Settings `json:"settings"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings := s.dlpSettings(r)
	if input.Settings != nil {
		if err := input.Settings.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_dlp", err.Error())
			return
		}
		settings = *input.Settings
	}
	// A sample is scanned with every configured class regardless of the master
	// switch: an administrator testing a rule has not turned the scanner on yet.
	settings.Enabled = true
	result := dlp.Scan(settings, input.Text)
	dlp.SortFindings(result.Findings)
	writeJSON(w, http.StatusOK, result)
}

// maxReportedFindings bounds one report. A gateway that found a thousand classes
// in one payload has found the same handful a thousand times.
const maxReportedFindings = 32

// reportDLPEvent receives a finding from an in-Pod gateway.
//
// Tool calls never pass through the control plane, so without this the scanning
// that happens in the Pod would only ever appear in that Pod's log — which is
// exactly where nobody looks, and nowhere an auditor can be shown. The gateway
// authenticates with the runtime's own token, the same way it asks for approvals.
func (s *Server) reportDLPEvent(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.runtimeFromGatewayToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Runtime 토큰을 확인할 수 없습니다.")
		return
	}
	var input struct {
		RuntimeID string `json:"runtimeId"`
		Event     struct {
			Server    string        `json:"server"`
			Tool      string        `json:"tool"`
			Direction string        `json:"direction"`
			Blocked   bool          `json:"blocked"`
			Truncated bool          `json:"truncated"`
			Findings  []dlp.Finding `json:"findings"`
		} `json:"event"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// The body's runtime id is a cross-check, never the authority: the token is.
	if input.RuntimeID != "" && input.RuntimeID != runtime.ID {
		writeError(w, http.StatusForbidden, "runtime_mismatch", "다른 Runtime의 기록을 보고할 수 없습니다.")
		return
	}
	findings := input.Event.Findings
	if len(findings) > maxReportedFindings {
		findings = findings[:maxReportedFindings]
	}
	// What the gateway did to the call, not what it might have done: a class set
	// to 기록만 leaves the tool call exactly as the agent wrote it.
	outcome := dlp.Result{Blocked: input.Event.Blocked, Findings: input.Event.Findings}.Outcome()
	details := map[string]any{
		"server": input.Event.Server, "tool": input.Event.Tool, "direction": input.Event.Direction,
		"truncated": input.Event.Truncated, "findings": findings, "runtimeId": runtime.ID,
	}
	// The actor is the agent's owner: the finding is about their agent's traffic,
	// and an audit trail whose actor is always "system" cannot be filtered by the
	// person who has to answer for it.
	owner, err := s.store.UserByID(r.Context(), runtime.OwnerID)
	var actor *store.User
	if err == nil {
		actor = &owner
	}
	s.store.Audit(r.Context(), actor, "dlp.tool", "agent", runtime.AgentID, outcome, clientIP(r), details)
	s.logger.Warn("sensitive data found on a tool call", "runtime", runtime.ID, "agent", runtime.AgentID,
		"server", input.Event.Server, "tool", input.Event.Tool, "outcome", outcome, "findings", len(findings))
	writeJSON(w, http.StatusAccepted, map[string]any{"recorded": true})
}

// scannedClasses is what this configuration actually inspects.
//
// A class mapped to "off" is listed and does nothing, which is the same silence
// as not listing it — so it does not count here either.
func scannedClasses(settings dlp.Settings) []string {
	scanned := []string{}
	for class, action := range settings.Classes {
		if action != "" && action != dlp.Off {
			scanned = append(scanned, class)
		}
	}
	sort.Strings(scanned)
	return scanned
}
