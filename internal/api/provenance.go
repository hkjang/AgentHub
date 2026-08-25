package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/execution"

	"github.com/hkjang/AgentHub/internal/store"
)

// adminProvenance says where this deployment sends its account of what it
// decided, and never says the credential it sends with it — the same rule the
// personal vault keeps: a secret that can be read back is a secret in every log
// that ever printed a response.
func (s *Server) adminProvenance(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.ProvenanceEndpoint(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoint":        settings.Endpoint,
		"header":          settings.Header,
		"tokenConfigured": settings.Token != "",
		"events":          store.ProvenanceEvents,
	})
}

// inheritProvenanceToken keeps the stored credential when a screen sends the
// header without it, because a screen that was never shown the token cannot send
// it back. Applied before the pairing is checked: the other order refuses the
// ordinary save — measured, saving an unchanged endpoint answered
// invalid_provenance_credential and the branch that keeps the token never ran.
func (s *Server) inheritProvenanceToken(r *http.Request, settings store.ProvenanceSettings) store.ProvenanceSettings {
	if settings.Token == "" && settings.Header != "" {
		existing, err := s.store.ProvenanceEndpoint(r.Context())
		if err == nil {
			settings.Token = existing.Token
		}
	}
	return settings
}

// putProvenance configures the sink, or turns it off with an empty address.
func (s *Server) putProvenance(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Endpoint string `json:"endpoint"`
		Header   string `json:"header"`
		Token    string `json:"token"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings := store.ProvenanceSettings{
		Endpoint: strings.TrimSpace(input.Endpoint),
		Header:   strings.TrimSpace(input.Header),
		Token:    input.Token,
	}
	if settings.Endpoint != "" {
		// An address the dispatcher cannot post to would be retried until the
		// event dead-letters, and the person who typed it would hear about it
		// from a notification an hour later rather than from this answer.
		address, err := url.Parse(settings.Endpoint)
		if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
			writeError(w, http.StatusBadRequest, "invalid_provenance_endpoint",
				"결정 기록을 받을 주소는 http 또는 https로 시작하는 완전한 주소여야 합니다.")
			return
		}
	}
	settings = s.inheritProvenanceToken(r, settings)
	if (settings.Header == "") != (settings.Token == "") {
		writeError(w, http.StatusBadRequest, "invalid_provenance_credential",
			"인증 헤더와 값은 함께 지정하거나 함께 비워 주세요.")
		return
	}
	if err := s.store.PutSetting(r.Context(), store.ProvenanceSettingKey, settings, nil, u.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "provenance.update", "provenance", "", "success", clientIP(r),
		map[string]any{"endpoint": settings.Endpoint, "credential": settings.Header != ""})
	s.logger.Warn("decision export settings changed", "by", u.Username, "endpoint", settings.Endpoint)
	writeJSON(w, http.StatusOK, map[string]any{"endpoint": settings.Endpoint, "tokenConfigured": settings.Token != ""})
}

// testProvenance sends one obviously-labelled record to the address on the
// screen and reports what the receiver said.
//
// Without it, a mistyped address is silent: the export fails inside the
// dispatcher, is retried, and eventually dead-letters an event nobody is
// watching, so the operator's first evidence that their audit trail was never
// arriving is an audit. This is the same request the dispatcher makes.
func (s *Server) testProvenance(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Endpoint string `json:"endpoint"`
		Header   string `json:"header"`
		Token    string `json:"token"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings := s.inheritProvenanceToken(r, store.ProvenanceSettings{
		Endpoint: strings.TrimSpace(input.Endpoint),
		Header:   strings.TrimSpace(input.Header),
		Token:    input.Token,
	})
	if !settings.Configured() {
		writeError(w, http.StatusBadRequest, "invalid_provenance_endpoint",
			"보낼 주소를 먼저 입력해 주세요.")
		return
	}
	// A sample rather than a real decision: the address has not been proven yet,
	// and the first thing sent to an unproven address should not be somebody's
	// actual task. Every field carries a value so that a receiver rejecting the
	// shape says so now instead of at the first real ending.
	record := store.DecisionRecord{
		DecisionID:   "test:" + u.ID,
		OccurredAt:   time.Now().UTC(),
		Category:     "test",
		Scenario:     "연결 확인용 샘플 (실제 결정이 아닙니다)",
		Reasoning:    "AgentHub 관리자 화면에서 보낸 시험 전송입니다.",
		Outcome:      "test",
		Source:       "console",
		Agent:        "샘플 에이전트",
		AgentID:      "00000000-0000-0000-0000-000000000000",
		AgentVersion: 1,
		Model:        "sample-model",
		RuntimeImage: "sample",
		TaskID:       "00000000-0000-0000-0000-000000000000",
		OwnerID:      u.ID,
	}
	if err := execution.SendDecision(r.Context(), settings, s.dlpSettings(r), record); err != nil {
		s.store.Audit(r.Context(), &u, "provenance.test", "provenance", "", "failure", clientIP(r),
			map[string]any{"endpoint": settings.Endpoint, "error": err.Error()})
		writeError(w, http.StatusBadGateway, "provenance_unreachable", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "provenance.test", "provenance", "", "success", clientIP(r),
		map[string]any{"endpoint": settings.Endpoint})
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "샘플 결정 기록 한 건을 보냈고, 받는 쪽이 정상으로 답했습니다.",
		"record":  record,
	})
}
