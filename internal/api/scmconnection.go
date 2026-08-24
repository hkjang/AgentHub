package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// The credentials the platform uses to talk back to a forge.
//
// A token is written once and never read back. What comes out of here is
// whether a connection exists, which host it is for, when it was last used and
// what went wrong the last time — which is the difference between a review that
// found nothing and a token somebody revoked last week.

func (s *Server) scmConnections(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	items, err := s.store.SCMConnections(r.Context(), u.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "kinds": store.SCMKinds})
}

func (s *Server) putSCMConnection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Host    string `json:"host"`
		Kind    string `json:"kind"`
		APIBase string `json:"apiBase"`
		Token   string `json:"token"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Host) == "" || strings.TrimSpace(input.Token) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "호스트와 토큰이 필요합니다.")
		return
	}
	item, err := s.store.PutSCMConnection(r.Context(), u.ID, input.Host, input.Kind, input.APIBase, input.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.store.Audit(r.Context(), &u, "scm.connection.save", "scm_connection", item.ID, "success", clientIP(r),
		map[string]any{"host": item.Host, "kind": item.Kind})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteSCMConnection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteSCMConnection(r.Context(), u.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "scm.connection.delete", "scm_connection", id, "success", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}
