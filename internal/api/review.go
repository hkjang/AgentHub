package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/hkjang/AgentHub/internal/runtimetype"
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

// fixReviewFinding hands one finding to an agent that can change files.
//
// Finding something and fixing it are two runtimes' work — the review engine
// reads and reports, a coding agent edits — and what was missing between them
// was the handover. A person read a finding on one screen and retyped it into a
// task on another, losing the file, the line and the suggested code on the way,
// and nothing afterwards connected the two.
//
// It does not mark the finding fixed. Asking for a fix is not having one, and
// the finding stays open until somebody says otherwise or a later review stops
// reporting it.
func (s *Server) fixReviewFinding(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		AgentID string `json:"agentId"`
		// Priority is passed through so an urgent fix can jump the queue the same
		// way any other task can.
		Priority string `json:"priority"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := u.Role == "admin"
	finding, err := s.store.ReviewFindingByID(r.Context(), chi.URLParam(r, "id"), u.ID, admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if finding.FixTaskID != "" {
		writeError(w, http.StatusConflict, "fix_already_requested",
			"이 지적은 이미 수정 작업으로 넘겼습니다. 작업 대기열에서 진행 상황을 확인해 주세요.")
		return
	}
	agent, err := s.store.AgentByID(r.Context(), input.AgentID, u.ID, admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A runtime that cannot edit a file cannot fix anything, and handing it the
	// work would produce a task that runs, reports something reasonable and
	// changes nothing. Asked of the descriptor, so adding a backend that can edit
	// is a name in a list rather than another branch here.
	if !runtimetype.SupportsRunner(agent.RuntimeType, runtimetype.RunnerCLI) &&
		!runtimetype.SupportsRunner(agent.RuntimeType, runtimetype.RunnerACP) {
		writeError(w, http.StatusBadRequest, "agent_cannot_edit",
			runtimetype.Describe(agent.RuntimeType).Label+" 런타임은 파일을 고치지 못합니다. 코딩 에이전트를 골라 주세요.")
		return
	}

	title := fmt.Sprintf("리뷰 지적 수정: %s:%d", finding.FilePath, finding.StartLine)
	task, err := s.enqueueTask(w, r, u, agent.ID, title, reviewFixInput(finding), input.Priority, "review", nil)
	if err != nil {
		return
	}
	linked, err := s.store.LinkReviewFix(r.Context(), finding.ID, task.ID, u.ID, admin)
	if err != nil {
		// The task exists and is already queued. Saying it failed would leave
		// somebody asking again and getting a second one.
		s.logger.Error("a fix task could not be linked to its finding", "finding", finding.ID, "task", task.ID, "error", err)
		linked = finding
		linked.FixTaskID = task.ID
	}
	s.store.Audit(r.Context(), &u, "review.finding.fix", "review_finding", finding.ID, "success", clientIP(r),
		map[string]any{"taskId": task.ID, "agentId": agent.ID, "filePath": finding.FilePath, "severity": finding.Severity})
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task, "finding": linked})
}

// reviewFixInput is what the coding agent is told.
//
// Everything the review established travels with it — which file, which lines,
// what is wrong and what the reviewer suggested — because an agent that has to
// go and find the problem again is an agent that may fix a different one. The
// instruction is deliberately narrow: this task exists to address one finding,
// not to improve the file.
func reviewFixInput(finding store.ReviewFinding) string {
	lines := []string{
		fmt.Sprintf("%s 파일의 %d번째 줄에 대한 코드 리뷰 지적을 수정해 주세요.", finding.FilePath, finding.StartLine),
		"",
		"지적 (" + finding.Severity + " · " + finding.Category + "):",
		finding.Message,
	}
	if finding.ExistingCode != "" {
		lines = append(lines, "", "현재 코드:", finding.ExistingCode)
	}
	if finding.Suggestion != "" {
		lines = append(lines, "", "리뷰가 제안한 코드:", finding.Suggestion,
			"", "제안은 참고입니다 — 이 코드베이스에서 맞지 않으면 더 나은 방법으로 고쳐 주세요.")
	}
	lines = append(lines, "",
		"이 지적 하나만 다루세요. 같은 파일의 다른 부분을 함께 고치면 무엇이 이 수정인지 알 수 없게 됩니다.",
		"고친 뒤에는 무엇을 왜 바꿨는지 한 문단으로 알려 주세요.")
	return strings.Join(lines, "\n")
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
