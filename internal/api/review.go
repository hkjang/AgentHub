package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/store"
)

// Reading and deciding about what a review found.
//
// A review's result is not the run's answer, so it is not read from the run's
// timeline. It is a list of things to look at, and what a person does with each
// — this one is real, this one is not — is the record that makes the next
// review's noise measurable.

// reviewFindings returns one review's findings and what it covered.
//
// The coverage travels with them because an empty list means two opposite
// things: a review that found nothing wrong, and a review that could not read
// anything. Sending only the findings would make the console guess.
func (s *Server) reviewFindings(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	runID := chi.URLParam(r, "id")
	admin := u.Role == "admin"
	items, err := s.store.ReviewFindings(r.Context(), runID, u.ID, admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	coverage, err := s.store.ReviewRunByID(r.Context(), runID)
	if err != nil && err != store.ErrNotFound {
		writeStoreError(w, err)
		return
	}
	counts := map[string]int{}
	open := 0
	for _, item := range items {
		counts[item.Severity]++
		if item.Status == "open" {
			open++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "coverage": coverage, "severityCounts": counts, "open": open,
		"severities": store.ReviewSeverities, "categories": store.ReviewCategories,
	})
}

// decideReviewFinding records what a person concluded about one finding.
func (s *Server) decideReviewFinding(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Decision string `json:"decision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.store.DecideReviewFinding(r.Context(), chi.URLParam(r, "id"), u.ID, input.Decision, u.Role == "admin")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.store.Audit(r.Context(), &u, "review.finding."+input.Decision, "review_finding", item.ID, "success", clientIP(r),
		map[string]any{"runId": item.RunID, "filePath": item.FilePath, "severity": item.Severity})
	writeJSON(w, http.StatusOK, item)
}
