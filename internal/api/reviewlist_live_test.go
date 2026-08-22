package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// What every review has found, across every review.
//
// A finding could only be reached by knowing which run produced it, so somebody
// who ran three reviews yesterday had no way to ask what they had left. This
// checks the list against a running deployment — and in particular that its
// counts are of the table the filter selects rather than of the page it
// returned, which is the mistake the notification bell was fixed for and the
// review queue after it.
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run FindingList -v
func TestTheFindingListCountsTheTableNotThePage(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check the finding list against a running deployment")
	}
	client := login(t, base)
	read := func(path string) struct {
		Items []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
			Status   string `json:"status"`
		} `json:"items"`
		Total          int            `json:"total"`
		OpenBySeverity map[string]int `json:"openBySeverity"`
	} {
		t.Helper()
		body, status := client.get(path)
		if status != http.StatusOK {
			t.Fatalf("%s answered %d", path, status)
		}
		var page struct {
			Items []struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
				Status   string `json:"status"`
			} `json:"items"`
			Total          int            `json:"total"`
			OpenBySeverity map[string]int `json:"openBySeverity"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Two pages, because the two properties want opposite things. Counting the
	// table rather than the page can only be told apart on a page smaller than
	// the table. Ordering can only be told apart on a page that reaches the lower
	// severities — the alphabetical ordering this really guards against,
	// critical < high < low < medium, first goes wrong where medium follows low,
	// and a page that stops before medium proves nothing.
	page := read("/api/v1/review-findings?limit=5")
	if page.Total <= len(page.Items) {
		t.Fatalf("this deployment holds %d finding(s), which fits in one page — the check cannot tell a count of the table from a count of the page", page.Total)
	}
	counted := 0
	for _, count := range page.OpenBySeverity {
		counted += count
	}
	if counted != page.Total {
		t.Errorf("the severity counts add up to %d against a total of %d; they are counting the rows that were fetched", counted, page.Total)
	}
	// Worst first, or the page is a list of low-severity noise with the critical
	// findings on page four.
	whole := read("/api/v1/review-findings?limit=200")
	spanned := map[string]bool{}
	for _, item := range whole.Items {
		spanned[item.Severity] = true
	}
	if len(spanned) < 3 {
		t.Fatalf("these findings span only %v; an ordering check needs ones that reach the lower severities", spanned)
	}
	worst := ""
	for _, item := range whole.Items {
		if worst == "" {
			worst = item.Severity
		}
		if rank(item.Severity) < rank(worst) {
			t.Errorf("severity %s appears after %s; the list is not worst-first", item.Severity, worst)
			break
		}
		worst = item.Severity
	}
	// Every finding on the default page is one somebody still has to deal with.
	for _, item := range page.Items {
		if item.Status != "open" {
			t.Errorf("the default list carries a finding already marked %q", item.Status)
			break
		}
	}

	// A filter has to narrow the counts with the list. A page whose totals stay
	// at the unfiltered number reads as "there are still 60 of these".
	narrowed := read("/api/v1/review-findings?severity=critical&limit=200")
	if narrowed.Total >= page.Total {
		t.Errorf("filtering to critical returned %d of %d; the filter did not narrow the total", narrowed.Total, page.Total)
	}
	for severity := range narrowed.OpenBySeverity {
		if severity != "critical" {
			t.Errorf("the counts under a critical filter still carry %q", severity)
		}
	}

	// A decision the platform does not have is the request being wrong.
	raw, status := client.do(http.MethodPost, "/api/v1/review-findings/"+page.Items[0].ID+"/decision", map[string]string{"decision": "open"})
	if status != http.StatusBadRequest {
		t.Errorf("an unknown decision answered %d, want 400: %s", status, first(raw, 160))
	}
	t.Logf("%d findings, counts %v, filtered to %d", page.Total, page.OpenBySeverity, narrowed.Total)
}

func rank(severity string) int {
	for index, name := range []string{"critical", "high", "medium", "low"} {
		if name == severity {
			return index
		}
	}
	return 9
}
