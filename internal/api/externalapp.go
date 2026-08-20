package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// The catalog of applications the platform drives but does not run.
//
// It is administered like the model endpoints are — an address, a credential the
// console never reads back, and an enabled flag — because that is what it is: a
// place the platform sends work to, owned by somebody else.

func (s *Server) adminExternalApps(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ExternalApps(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "providers": store.ExternalAppProviders, "kinds": store.ExternalAppKinds,
	})
}

func (s *Server) saveExternalApp(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	var input struct {
		store.ExternalApp
		Secret *string `json:"secret"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_app", "앱 이름은 1~80자여야 합니다.")
		return
	}
	if input.Provider == "" {
		input.Provider = store.ExternalAppProviders[0]
	}
	if input.AppKind == "" {
		input.AppKind = store.ExternalAppKinds[0]
	}
	if !contains(store.ExternalAppProviders, input.Provider) || !contains(store.ExternalAppKinds, input.AppKind) {
		writeError(w, http.StatusBadRequest, "invalid_app", "앱 종류를 확인해 주세요.")
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(input.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, http.StatusBadRequest, "invalid_app_url", "앱 주소를 확인해 주세요. 예) https://dify.internal")
		return
	}
	// A credential is required to create one — an app the platform cannot
	// authenticate to is a row that fails at three in the morning instead of now.
	if input.ID == "" && (input.Secret == nil || strings.TrimSpace(*input.Secret) == "") {
		writeError(w, http.StatusBadRequest, "invalid_app_secret", "앱의 API 키를 입력해 주세요.")
		return
	}
	saved, err := s.store.UpsertExternalApp(r.Context(), input.ExternalApp, input.Secret)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &user, "external_app.save", "external-app", saved.ID, "success", clientIP(r),
		map[string]any{"provider": saved.Provider, "kind": saved.AppKind})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteExternalApp(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteExternalApp(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &user, "external_app.delete", "external-app", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// externalApps lists what a person can choose in a Goal. It is a read for every
// signed-in user rather than an admin route, because choosing one is part of
// configuring an agent — and it carries no credentials.
func (s *Server) externalApps(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ExternalApps(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	enabled := make([]store.ExternalApp, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": enabled})
}
