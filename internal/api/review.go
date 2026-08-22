package api

import (
	"net/http"
	"strconv"
	"strings"

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

// reviewFindingList answers "what is still open", across every review.
//
// The findings of one run are reachable from that run. This is the other
// question, and until there was a list nobody could ask it: somebody who ran
// three reviews yesterday had no way to see what they had left. It opens on the
// open ones for the same reason — a page that starts with a year of dismissed
// findings is a page nobody reads twice.
func (s *Server) reviewFindingList(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	filter := store.ReviewFindingFilter{
		OwnerID: u.ID, AgentID: query.Get("agentId"),
		Severity: query.Get("severity"), Category: query.Get("category"),
		Status: query.Get("status"), Limit: limit, Offset: offset,
	}
	if filter.Severity != "" && !containsValue(store.ReviewSeverities, filter.Severity) {
		writeError(w, http.StatusBadRequest, "invalid_filter", "심각도를 확인해 주세요.")
		return
	}
	if filter.Category != "" && !containsValue(store.ReviewCategories, filter.Category) {
		writeError(w, http.StatusBadRequest, "invalid_filter", "분류를 확인해 주세요.")
		return
	}
	// Looking across the deployment is an administrator's view of somebody else's
	// work, so it is asked for explicitly rather than implied by the role.
	everyone := u.Role == "admin" && query.Get("scope") == "all"
	page, err := s.store.ReviewFindingsFor(r.Context(), filter, everyone)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		store.ReviewFindingPage
		Severities []string `json:"severities"`
		Categories []string `json:"categories"`
	}{ReviewFindingPage: page, Severities: store.ReviewSeverities, Categories: store.ReviewCategories})
}

func containsValue(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
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
	// A decision the platform does not have is the request being wrong, not the
	// platform breaking. Answered here so it is a 400 with the list of what is
	// allowed, rather than a 500 carrying the store's own sentence.
	if !containsValue(store.ReviewFindingDecisions, input.Decision) {
		writeError(w, http.StatusBadRequest, "invalid_decision",
			"처리 상태는 "+strings.Join(store.ReviewFindingDecisions, ", ")+" 중 하나여야 합니다.")
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
