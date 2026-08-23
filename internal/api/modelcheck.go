package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/modelprobe"
)

// Whether a model endpoint is actually there.
//
// An administrator registers a base URL and a model name, and the platform finds
// out whether either is right at the moment a task runs — which is usually at
// night, on somebody else's agent, as a failure that reads like the agent's
// fault. On the cluster these releases were tested against, forty-five of
// sixty-five failed runs in one window were the same connection refused to a
// gateway that had stopped; every one of them was reported as a task failure.
//
// So the endpoint can be asked directly, and asked the second question too: the
// model list it answers with either contains the default model somebody typed or
// it does not. A typo there is invisible until inference time, and then it is an
// error from the provider that names neither the setting nor the screen.

func (s *Server) modelCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	endpoint, key, err := s.store.ModelEndpointByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	verdict, detail, models := modelprobe.Ask(r.Context(), endpoint.BaseURL, key, endpoint.DefaultModel)
	// Kept, so the answer survives the page reload that used to erase it — and so
	// the readiness advice elsewhere can say whether anybody has ever asked.
	if _, _, err := s.store.RecordModelEndpointHealth(r.Context(), endpoint.ID, verdict, detail); err != nil {
		s.logger.Warn("a model endpoint's health could not be recorded", "endpoint", endpoint.ID, "error", err)
	}
	s.store.Audit(r.Context(), &u, "model.check", "model", endpoint.ID, verdict, clientIP(r),
		map[string]any{"baseUrl": endpoint.BaseURL, "detail": detail})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": endpoint.ID, "verdict": verdict, "detail": detail, "models": models,
	})
}

// shortError keeps the first line and drops the wrapping Go adds, which names
// this platform's own call stack rather than anything an administrator set.
func shortError(value string) string {
	if at := strings.Index(value, "\n"); at >= 0 {
		value = value[:at]
	}
	if at := strings.LastIndex(value, ": "); at >= 0 && len(value)-at < 80 {
		return value[at+2:]
	}
	return value
}

func firstFew(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return append(values[:limit:limit], "…")
}
