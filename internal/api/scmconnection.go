package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/execution"
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

	// Asked now, while the person who pasted it is still here. A token is used
	// weeks later by a review at night, and a wrong one is otherwise first
	// noticed as a review that said nothing where somebody was waiting to read
	// it.
	//
	// The connection is kept either way. A control plane that cannot reach the
	// forge from where it runs is a real deployment, and refusing to save the
	// credential would leave that deployment with no way to configure this at
	// all — so what is stored is the answer, not a verdict on whether to store.
	account, checkErr := execution.CheckSCMConnection(r.Context(), execution.SCMCheckClient, item, input.Token)
	failure := ""
	if checkErr != nil {
		failure = checkErr.Error()
	}
	if err := s.store.RecordSCMUse(r.Context(), item.ID, failure); err != nil {
		s.logger.Warn("the forge check could not be recorded", "connection", item.ID, "error", err)
	}
	item.LastError = failure
	writeJSON(w, http.StatusOK, map[string]any{"connection": item, "account": account, "checkFailed": failure})
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
