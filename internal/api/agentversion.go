package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// Agent versions and promotion.
//
// Saving an agent used to be the whole release process: the next scheduled run
// executed whatever the definition said at that moment, evaluated or not, and the
// only way back from a bad edit was to remember what it used to say.

// snapshotAgent records the definition that was just saved.
//
// A failure here is logged rather than surfaced: the definition is already saved,
// and refusing the caller's edit because its history could not be written would
// trade the working change for the record of it.
func (s *Server) snapshotAgent(r *http.Request, agent store.Agent, note, actorID string) {
	if err := s.store.RecordAgentVersion(r.Context(), agent, note, actorID); err != nil {
		s.logger.Warn("agent version could not be recorded", "agent", agent.ID, "version", agent.Version, "error", err)
	}
}

// agentVersions lists the saved definitions and where the agent stands.
func (s *Server) agentVersions(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	versions, err := s.store.AgentVersions(r.Context(), agent.ID, 50)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	release, err := s.store.AgentReleaseState(r.Context(), agent.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions, "release": release})
}

// promoteAgentVersion approves one version for production, or turns the gate
// itself on and off.
func (s *Server) promoteAgentVersion(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input struct {
		Version *int  `json:"version"`
		Require *bool `json:"requirePromotion"`
		// Force skips the evaluation requirement. Administrators only, and never
		// silently: the reason is stored with the promotion and audited.
		Force bool   `json:"force"`
		Note  string `json:"note"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Require != nil {
		if _, err := s.store.SetAgentPromotionGate(r.Context(), agent.ID, *input.Require); err != nil {
			writeStoreError(w, err)
			return
		}
		s.store.Audit(r.Context(), &u, "agent.promotion.gate", "agent", agent.ID, "success", clientIP(r), map[string]any{"required": *input.Require})
	}
	if input.Version == nil {
		release, err := s.store.AgentReleaseState(r.Context(), agent.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, release)
		return
	}
	note, refusal := promotionNote(u.Role, input.Note, input.Force)
	if refusal != "" {
		switch refusal {
		case "force_requires_admin":
			writeError(w, http.StatusForbidden, refusal, "사전검증 없이 승격하는 것은 관리자만 할 수 있습니다.")
		default:
			writeError(w, http.StatusBadRequest, refusal, "사전검증 없이 승격하려면 사유를 입력해야 합니다.")
		}
		return
	}
	release, err := s.store.PromoteAgentVersion(r.Context(), agent.ID, *input.Version, u.ID, note, input.Force)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "version_not_found", "해당 버전을 찾을 수 없습니다.")
			return
		}
		writeError(w, http.StatusConflict, "promotion_blocked", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "agent.promote", "agent", agent.ID, "success", clientIP(r),
		map[string]any{"version": *input.Version, "force": input.Force, "note": note})
	s.logger.Info("agent version promoted", "agent", agent.ID, "version", *input.Version, "force", input.Force)
	writeJSON(w, http.StatusOK, s.withReleasedTasks(r, agent.ID, release))
}

// promotionNote decides whether an override may proceed and what it will be
// recorded as, and returns the refusal code when it may not.
//
// Both refusals are the point of the override rather than paperwork around it:
// one limits who may skip a pre-flight evaluation, the other makes sure that
// skipping it is never anonymous. The rule is kept apart from the handler so it
// can be read and tested as the rule it is.
func promotionNote(role, note string, force bool) (string, string) {
	note = strings.TrimSpace(note)
	if !force {
		return note, ""
	}
	if role != "admin" {
		return "", "force_requires_admin"
	}
	if note == "" {
		return "", "force_requires_note"
	}
	return "검증 생략 승격: " + note, ""
}

// restoreAgentVersion puts an old definition back as the live one.
func (s *Server) restoreAgentVersion(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	agent, err := s.store.AgentByID(r.Context(), chi.URLParam(r, "id"), u.ID, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid_version", "버전 번호를 확인해 주세요.")
		return
	}
	restored, err := s.store.RestoreAgentVersion(r.Context(), agent.ID, version, u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "agent.restore", "agent", agent.ID, "success", clientIP(r),
		map[string]any{"from": version, "version": restored.Version})
	s.logger.Info("agent version restored", "agent", agent.ID, "from", version, "version", restored.Version)
	// The Pod runs the previous definition until it is recreated, which is the
	// same caveat every other edit carries.
	response := map[string]any{
		"agent":   restored,
		"warning": "이전 정의를 새 버전으로 복원했습니다. 실행 중인 Runtime은 재시작 후 적용됩니다.",
	}
	if released := s.releaseBlocked(r, agent.ID); released > 0 {
		response["releasedTasks"] = released
	}
	writeJSON(w, http.StatusOK, response)
}

// releaseBlocked puts back the tasks the gate was holding for this agent.
//
// A promotion is the answer to the question those tasks are waiting on, so they
// go back on the queue on the spot rather than needing to be recreated. A failure
// here is logged rather than surfaced: the promotion itself succeeded, and the
// tasks are still held, which is recoverable and visible.
func (s *Server) releaseBlocked(r *http.Request, agentID string) int {
	released, err := s.store.ReleaseBlockedTasks(r.Context(), agentID)
	if err != nil {
		s.logger.Warn("blocked tasks could not be released", "agent", agentID, "error", err)
		return 0
	}
	if released > 0 {
		s.logger.Info("blocked tasks released by a promotion", "agent", agentID, "tasks", released)
	}
	return released
}

// withReleasedTasks reports the release alongside the new promotion state, so
// the console can say what the promotion just started.
func (s *Server) withReleasedTasks(r *http.Request, agentID string, release store.AgentRelease) map[string]any {
	response := map[string]any{
		"promotedVersion":  release.PromotedVersion,
		"promotedAt":       release.PromotedAt,
		"promotedBy":       release.PromotedBy,
		"promotionNote":    release.PromotionNote,
		"requirePromotion": release.RequirePromotion,
		"currentVersion":   release.Current,
	}
	if released := s.releaseBlocked(r, agentID); released > 0 {
		response["releasedTasks"] = released
	}
	return response
}
