package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// An export has to carry the whole answer, or say that it does not.
//
// The audit CSV asked the store for five thousand rows; the store saw a number
// above its page ceiling and reset it to the default hundred. An auditor
// downloading a compliance trail with nineteen hundred entries in it got a
// hundred lines, in a file with nothing in it to say so, and the audit record
// the export wrote about itself had the real total sitting next to the truncated
// count.
//
// Reading the code would not have caught it: both halves are reasonable on their
// own and the bug lives in the gap. So this asks the running deployment, and
// compares the file against the number the API itself reports:
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run ExportCarries -v
func TestAnExportCarriesTheWholeTrail(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check the exports against a running deployment")
	}
	client := login(t, base)

	body, status := client.get("/api/v1/admin/audit?limit=1")
	if status != http.StatusOK {
		t.Fatalf("the audit trail could not be read (%d); nothing was checked", status)
	}
	var page struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total <= 100 {
		t.Fatalf("this deployment's trail holds %d rows, which fits in one page — the check cannot tell a whole export from a truncated one", page.Total)
	}

	raw, status := client.get("/api/v1/admin/audit/export")
	if status != http.StatusOK {
		t.Fatalf("the export returned %d", status)
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\xef\xbb\xbf")))
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("the export is not readable as CSV: %v", err)
	}
	// The notice a truncated file carries is a last row whose first cell says so
	// and whose remaining cells are empty — it is padded to the file's width,
	// because a short row would make the download unparseable exactly when
	// somebody most needs to read it.
	carried := len(rows) - 1
	truncated := ""
	if last := rows[len(rows)-1]; carried > 0 && strings.Contains(last[0], "잘렸습니다") || carried > 0 && strings.Contains(last[0], "완전하지 않습니다") {
		truncated, carried = rows[len(rows)-1][0], carried-1
	}
	switch {
	case truncated != "":
		t.Logf("the export stopped and said so: %s", truncated)
	case carried < page.Total:
		t.Errorf("the export carried %d of %d rows and said nothing; a truncated compliance file looks exactly like a complete one", carried, page.Total)
	default:
		t.Logf("the export carried all %d rows the trail reports", carried)
	}

	// The trail records what its own export did, and that record is how somebody
	// finds out afterwards whether the file they were handed was the whole thing.
	body, status = client.get("/api/v1/admin/audit?action=admin.audit.export&limit=1")
	if status != http.StatusOK {
		t.Fatalf("the export's own audit record could not be read (%d)", status)
	}
	var recorded struct {
		Items []struct {
			Details map[string]any `json:"details"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &recorded); err != nil {
		t.Fatal(err)
	}
	if len(recorded.Items) == 0 {
		t.Fatal("the export left no audit record, so nothing says what was handed over")
	}
	details := recorded.Items[0].Details
	complete, ok := details["complete"].(bool)
	if !ok {
		t.Fatalf("the export's audit record does not say whether the file was complete: %v", details)
	}
	if complete != (truncated == "") {
		t.Errorf("the file and its audit record disagree: record says complete=%v, file says %q", complete, truncated)
	}
	t.Logf("the export's own record: %v", details)
}
