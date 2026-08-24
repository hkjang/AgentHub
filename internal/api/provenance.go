package api

import (
	"net/http"
	"net/url"
	"strings"

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
		"events":          []string{store.EventTaskCompleted, store.EventTaskFailed},
	})
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
	// A header with no token means "leave the credential alone", because a screen
	// that was never shown the token cannot send it back. Inherited before the
	// pairing is checked: the other order refuses the ordinary save — measured,
	// saving an unchanged endpoint answered invalid_provenance_credential and the
	// branch that keeps the token could never run.
	if settings.Token == "" && settings.Header != "" {
		existing, err := s.store.ProvenanceEndpoint(r.Context())
		if err == nil {
			settings.Token = existing.Token
		}
	}
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
