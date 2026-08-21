package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Administration: the numbers, and the trail.
//
// Everything an operator needed was already recorded, but only ever shown as one
// person's slice of it. Answering "is the platform healthy, who is spending what,
// and what is stuck" meant reading five screens and adding rows up by eye, which
// is why nobody knew until something failed.

// reportWindow resolves the from/to of a report.
//
// The two forms exist because operators ask in two ways: a rolling window
// ("last 7 days") for a dashboard that is glanced at, and explicit endpoints for
// a month somebody is reconciling.
func reportWindow(r *http.Request, defaultDays int) (time.Time, time.Time, error) {
	query := r.URL.Query()
	// The end is resolved first so that a length is measured back from it: asking
	// for two days ending last Friday is a normal request, and computing the start
	// from "now" instead would produce a window that ends before it begins.
	to := time.Now().UTC()
	if raw := strings.TrimSpace(query.Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("to는 RFC3339 시각이어야 합니다")
		}
		to = parsed.UTC()
	}
	from := to.AddDate(0, 0, -defaultDays)
	if raw := strings.TrimSpace(query.Get("days")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 1 || days > maxUsageWindowDays {
			return time.Time{}, time.Time{}, fmt.Errorf("조회 기간은 1~%d일 사이의 숫자여야 합니다", maxUsageWindowDays)
		}
		from = to.AddDate(0, 0, -days)
	}
	if raw := strings.TrimSpace(query.Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("from은 RFC3339 시각이어야 합니다")
		}
		from = parsed.UTC()
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("조회 종료 시각은 시작 시각보다 뒤여야 합니다")
	}
	if to.Sub(from) > maxUsageWindowDays*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("조회 기간은 최대 %d일입니다", maxUsageWindowDays)
	}
	return from, to, nil
}

// adminOverview is the whole deployment in one response.
func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	from, to, err := reportWindow(r, 7)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_window", err.Error())
		return
	}
	overview, err := s.store.PlatformOverview(r.Context(), from, to)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The quota policy travels with the numbers: spend without the limit beside
	// it is a figure nobody can act on.
	policy, policyErr := s.store.ExecutionPolicy(r.Context())
	if policyErr != nil {
		s.logger.Warn("execution quota policy is unreadable", "error", policyErr)
	}
	// Which departments are nearly full. The bars on the department screen say the
	// same thing, but only to somebody who went looking; this is the screen an
	// operator already has open when a colleague is refused.
	pressure, pressureErr := s.store.DepartmentsUnderPressure(r.Context())
	if pressureErr != nil {
		s.logger.Warn("department quota pressure is unreadable", "error", pressureErr)
	}
	operations := s.operationsSettings(r)
	writeJSON(w, http.StatusOK, struct {
		store.PlatformOverview
		Quota    any `json:"quota"`
		Pressure any `json:"quotaPressure"`
		Paused   any `json:"paused"`
	}{PlatformOverview: overview, Pressure: pressure, Quota: map[string]any{
		"windowDays":  int(store.QuotaWindow.Hours() / 24),
		"maxRunning":  policy.MaxRunningTasksPerUser,
		"tokenBudget": policy.TokenBudgetPerUser,
		"costBudget":  policy.CostBudgetPerUser,
	}, Paused: map[string]any{
		"paused": operations.Paused, "reason": operations.Reason,
		"by": operations.PausedBy, "at": operations.PausedAt,
	}})
}

// adminSpend is the bill broken down further than the overview shows, for the
// screen an operator opens when the overview says something is expensive.
func (s *Server) adminSpend(w http.ResponseWriter, r *http.Request) {
	from, to, err := reportWindow(r, 30)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_window", err.Error())
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 50
	}
	spend, err := s.store.PlatformSpend(r.Context(), from, to, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "spend": spend})
}

// adminSpendExport writes the same breakdown as CSV.
//
// A spreadsheet is where a bill is actually reconciled, and an offline site
// cannot pipe this into anything else.
func (s *Server) adminSpendExport(w http.ResponseWriter, r *http.Request) {
	from, to, err := reportWindow(r, 30)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_window", err.Error())
		return
	}
	spend, err := s.store.PlatformSpend(r.Context(), from, to, 200)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	u, _ := userFromContext(r.Context())
	s.store.Audit(r.Context(), &u, "admin.usage.export", "usage", "", "success", clientIP(r),
		map[string]any{"from": from, "to": to})
	writeCSV(w, fmt.Sprintf("agenthub-usage-%s.csv", to.Format("20060102")),
		[]string{"구분", "ID", "이름", "실행", "입력토큰", "출력토큰", "비용", "통화", "단가적용"},
		func(out *csv.Writer) {
			for _, group := range []struct {
				label string
				rows  []store.SpendRow
			}{{"사용자", spend.Users}, {"에이전트", spend.Agents}, {"모델", spend.Models}} {
				for _, row := range group.rows {
					_ = out.Write([]string{group.label, row.ID, row.Name,
						strconv.Itoa(row.Runs), strconv.FormatInt(row.InputTokens, 10),
						strconv.FormatInt(row.OutputTokens, 10),
						strconv.FormatFloat(row.Cost, 'f', 4, 64), spend.Currency,
						boolText(row.Priced)})
				}
			}
			for _, point := range spend.Daily {
				_ = out.Write([]string{"일자", point.Day.Format("2006-01-02"), "",
					"", strconv.FormatInt(point.InputTokens, 10), strconv.FormatInt(point.OutputTokens, 10),
					strconv.FormatFloat(point.Cost, 'f', 4, 64), spend.Currency, ""})
			}
		})
}

// auditFilter reads the trail's filters off the query string.
func auditFilter(r *http.Request) (store.AuditFilter, error) {
	query := r.URL.Query()
	filter := store.AuditFilter{
		Actor:        query.Get("actor"),
		Action:       query.Get("action"),
		ResourceType: query.Get("resourceType"),
		ResourceID:   query.Get("resourceId"),
		Outcome:      query.Get("outcome"),
	}
	filter.Limit, _ = strconv.Atoi(query.Get("limit"))
	filter.Offset, _ = strconv.Atoi(query.Get("offset"))
	for _, bound := range []struct {
		name   string
		target **time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		raw := strings.TrimSpace(query.Get(bound.name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return store.AuditFilter{}, fmt.Errorf("%s는 RFC3339 시각이어야 합니다", bound.name)
		}
		value := parsed.UTC()
		*bound.target = &value
	}
	return filter, nil
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	filter, err := auditFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	page, err := s.store.AuditTrail(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	actions, err := s.store.AuditActions(r.Context())
	if err != nil {
		s.logger.Warn("audit action list is unreadable", "error", err)
		actions = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		store.AuditPage
		Actions []string `json:"actions"`
	}{AuditPage: page, Actions: actions})
}

// adminAuditExport writes the filtered trail as CSV, which is what an auditor
// asks for and what a compliance review keeps.
func (s *Server) adminAuditExport(w http.ResponseWriter, r *http.Request) {
	filter, err := auditFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	// An export is not a page: it carries the whole filtered result, up to a
	// bound that keeps one request from reading a year of rows into memory.
	filter.Limit, filter.Offset = 5000, 0
	page, err := s.store.AuditTrail(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	u, _ := userFromContext(r.Context())
	s.store.Audit(r.Context(), &u, "admin.audit.export", "audit", "", "success", clientIP(r),
		map[string]any{"rows": len(page.Items), "total": page.Total})
	writeCSV(w, fmt.Sprintf("agenthub-audit-%s.csv", time.Now().UTC().Format("20060102")),
		[]string{"시각", "수행자", "동작", "대상유형", "대상ID", "결과", "IP", "상세"},
		func(out *csv.Writer) {
			for _, item := range page.Items {
				details, _ := json.Marshal(item["details"])
				_ = out.Write([]string{
					text(item["occurredAt"]), text(item["actor"]), text(item["action"]),
					text(item["resourceType"]), text(item["resourceId"]), text(item["outcome"]),
					text(item["ipAddress"]), string(details),
				})
			}
		})
}

// writeCSV streams a download with a UTF-8 BOM.
//
// The BOM is there because Excel on a Korean Windows install reads a BOM-less
// UTF-8 file as the legacy code page, and an operator opening the export sees
// mojibake instead of names — which reads as a broken export rather than a
// spreadsheet setting.
func writeCSV(w http.ResponseWriter, filename string, header []string, rows func(*csv.Writer)) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	out := csv.NewWriter(w)
	_ = out.Write(header)
	rows(out)
	out.Flush()
}

func boolText(value bool) string {
	if value {
		return "예"
	}
	return "아니오"
}

// text renders one audit column for CSV.
func text(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case time.Time:
		return typed.Format(time.RFC3339)
	default:
		return fmt.Sprint(typed)
	}
}
