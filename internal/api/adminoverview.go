package api

import (
	"context"
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
	// The whole bill, not the top of it. This asked for two hundred rows per
	// breakdown, which is a console page rather than an export: a deployment
	// with more agents than that reconciled from a file that stopped, with
	// nothing in the file to say where.
	spend, err := s.store.PlatformSpend(r.Context(), from, to, store.PlatformSpendCeiling)
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
				if len(group.rows) >= store.PlatformSpendCeiling {
					_ = out.Write(noticeRow(fmt.Sprintf("%s 구분은 %d건에서 잘렸습니다 — 기간을 나눠 다시 받으세요.", group.label, store.PlatformSpendCeiling), 9))
				}
				for _, row := range group.rows {
					_ = out.Write([]string{group.label, row.ID, row.Name,
						strconv.Itoa(row.Runs), strconv.FormatInt(row.InputTokens, 10),
						strconv.FormatInt(row.OutputTokens, 10),
						strconv.FormatFloat(row.Cost, 'f', 4, 64), spend.Currency,
						boolText(row.Priced)})
				}
			}
			// The bill says how short it is, in the file somebody reconciles from.
			// A spreadsheet that adds up to less than the work done, with nothing in
			// it to say so, is the same confident zero the report exists to avoid.
			if spend.UnrecordedRuns > 0 {
				_ = out.Write(noticeRow(fmt.Sprintf("실행 %d건은 사용량이 단계에 기록되지 않아 이 합계에 빠져 있습니다(약 %d 토큰).",
					spend.UnrecordedRuns, spend.UnrecordedTokens), 9))
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
			return store.AuditFilter{}, fmt.Errorf("%s: RFC3339 시각이어야 합니다", bound.name)
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
	// An export is not a page: it carries the whole filtered result, read a page
	// at a time so a year of rows never sits in memory at once. If it does have
	// to stop, the file says so — a compliance review reads the file, not the
	// server's idea of how big it should have been.
	filter.Limit, filter.Offset = 0, 0
	var written, total int
	var walkErr error
	writeCSV(w, fmt.Sprintf("agenthub-audit-%s.csv", time.Now().UTC().Format("20060102")),
		[]string{"시각", "수행자", "동작", "대상유형", "대상ID", "결과", "IP", "상세"},
		func(out *csv.Writer) {
			written, total, walkErr = s.store.AuditTrailEach(r.Context(), filter, store.AuditExportCeiling, func(item map[string]any) error {
				details, _ := json.Marshal(item["details"])
				return out.Write([]string{
					text(item["occurredAt"]), text(item["actor"]), text(item["action"]),
					text(item["resourceType"]), text(item["resourceId"]), text(item["outcome"]),
					text(item["ipAddress"]), string(details),
				})
			})
			if notice := exportNotice(written, total, walkErr); notice != "" {
				_ = out.Write(noticeRow(notice, 8))
			}
		})
	u, _ := userFromContext(r.Context())
	outcome := "success"
	if walkErr != nil || written < total {
		outcome = "partial"
	}
	s.store.Audit(context.WithoutCancel(r.Context()), &u, "admin.audit.export", "audit", "", outcome, clientIP(r),
		map[string]any{"rows": written, "total": total, "complete": walkErr == nil && written == total})
}

// exportNotice is the last row of a file that is not the whole answer.
//
// A download that stops has no status code left to fail with and no page footer
// to put a warning in, so the warning goes where the person is looking: the
// bottom of the spreadsheet. Saying nothing is what made the old export
// dangerous — a hundred rows of a two-thousand-row trail look exactly like a
// complete file.
// noticeRow pads the warning out to the width of the file.
//
// A row with one cell in an eight-column file is not CSV any more: Excel shrugs,
// and everything that parses strictly — the spreadsheet's own importer, a script,
// this platform's own check — rejects the file outright. A truncated export that
// cannot be opened is a worse answer than a truncated one that can.
func noticeRow(message string, width int) []string {
	row := make([]string, width)
	row[0] = message
	return row
}

func exportNotice(written, total int, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("이 파일은 완전하지 않습니다 — %d건까지 기록한 뒤 오류로 중단됐습니다(%v). 조건을 좁혀 다시 받으세요.", written, err)
	case written < total:
		return fmt.Sprintf("이 파일은 %d건에서 잘렸습니다 — 조건에 맞는 기록은 %d건입니다. 기간이나 조건을 좁혀 나눠 받으세요.", written, total)
	default:
		return ""
	}
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
		// Milliseconds, because a trail is read to establish an order and RFC3339
		// alone rounds to the second: eighty-six rows of this deployment's
		// nineteen hundred are distinct events that printed as identical lines.
		return typed.Format("2006-01-02T15:04:05.000Z07:00")
	default:
		return fmt.Sprint(typed)
	}
}
