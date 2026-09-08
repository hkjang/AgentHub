package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/store"
)

// A finding the platform acted on and did not write down is one the operator
// cannot see. Both of these boundaries scanned their text and then dropped what
// they found on the floor unless they refused the send outright, so a class set
// to 기록만 — the documented way a site learns what its agents handle before it
// starts blocking — left no trace of the two paths that leave the building
// entirely, and a redaction rewrote the payload just as silently.
//
// Checked at the sending functions themselves, because that is where the scan
// happens and where what it found has to come back from.
func TestWhatTheScanFoundComesBackFromEveryBoundary(t *testing.T) {
	const rrn = "900101-1234568"
	auditing := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Audit}}
	redacting := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Redact}}
	blocking := dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}

	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer sink.Close()
	record := store.DecisionRecord{TaskID: "t1", Scenario: "민원인 " + rrn + " 환급 검토"}
	settings := store.ProvenanceSettings{Endpoint: sink.URL}

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Kind: "gitea", Host: strings.TrimPrefix(forge.URL, "http://"), APIBase: forge.URL + "/api/v1"}
	page := forge.URL + "/acme/store/pulls/1"
	comment := "리뷰 대상 코드에 " + rrn + " 이 있습니다"

	for _, boundary := range []struct {
		name string
		send func(dlp.Settings) (ContentOutcome, error)
	}{
		{"결정 기록", func(scan dlp.Settings) (ContentOutcome, error) {
			return SendDecision(context.Background(), settings, ContentGuard{Scan: scan}, record)
		}},
		{"리뷰 코멘트", func(scan dlp.Settings) (ContentOutcome, error) {
			return PostReviewComment(context.Background(), forge.Client(), connection, "s3cret", page, comment, ContentGuard{Scan: scan})
		}},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			// Nothing configured is almost every deployment, and it must produce
			// nothing to record rather than an entry saying a clean payload left.
			clean, err := boundary.send(dlp.Settings{})
			if err != nil {
				t.Fatalf("an unscanned send failed: %v", err)
			}
			if len(clean.Scan.Findings) != 0 {
				t.Errorf("a deployment with no scanner reported %d findings", len(clean.Scan.Findings))
			}

			// 기록만: the text goes out exactly as it was written, and the entry is
			// the only thing that says it carried anything.
			audited, err := boundary.send(auditing)
			if err != nil {
				t.Fatalf("an audited send failed: %v", err)
			}
			if len(audited.Scan.Findings) == 0 {
				t.Fatal("a class set to 기록만 found nothing to record, so the trail this screen points at stays empty")
			}
			if audited.Outcome() != dlp.OutcomeAudited {
				t.Errorf("the payload was recorded as %q rather than passed through", audited.Outcome())
			}
			if finding := audited.Scan.Findings[0]; finding.Class != "rrn" || finding.Action != dlp.Audit {
				t.Errorf("the finding does not say what was found or what was done: %+v", finding)
			}
			if strings.Contains(audited.Scan.Findings[0].Sample, rrn) {
				t.Errorf("the audit trail carries the value itself: %q", audited.Scan.Findings[0].Sample)
			}

			redacted, err := boundary.send(redacting)
			if err != nil {
				t.Fatalf("a redacted send failed: %v", err)
			}
			if redacted.Outcome() != dlp.OutcomeRedacted {
				t.Errorf("text was rewritten on its way out and reported as %q", redacted.Outcome())
			}

			refused, err := boundary.send(blocking)
			if err == nil {
				t.Fatal("a class configured to block was sent anyway")
			}
			if refused.Outcome() != dlp.OutcomeBlocked || len(refused.Scan.Findings) == 0 {
				t.Errorf("the refusal came back without what it found: %+v", refused)
			}
		})
	}
}

// The result coming back is only half of it: the caller holding the database has
// to write it down, and only once the send settled. A sink that answered with an
// error is retried by the dispatcher, and an entry per attempt would count one
// payload as many.
func TestBothSendersRecordWhatCameBack(t *testing.T) {
	for _, boundary := range []struct {
		file, send, event string
	}{
		{"provenance.go", "SendDecision(ctx, settings, d.contentGuard(ctx, record), record)", "scanEventExport"},
		{"scmpost.go", "PostReviewComment(ctx, scmHTTPClient, connection, token, task.SourceURL", "scanEventReview"},
	} {
		body, err := os.ReadFile(boundary.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		at := strings.Index(source, boundary.send)
		if at < 0 {
			t.Fatalf("%s no longer sends the way this guard reads it", boundary.file)
		}
		after := source[at:]
		if end := strings.Index(after, "\n}\n"); end > 0 {
			after = after[:end]
		}
		if !strings.Contains(after, "recordContentScan(") {
			t.Errorf("%s sends text and never records what the scan found in it", boundary.file)
		}
		if !strings.Contains(after, boundary.event) {
			t.Errorf("%s records under a name the DLP screen does not point at", boundary.file)
		}
		if !strings.Contains(after, "errors.As(err, &withheld)") {
			t.Errorf("%s records a finding without waiting for the send to settle", boundary.file)
		}
	}
}

// Two copies of a list is how the console and the server came to disagree about
// everything else in this codebase that has ever been written twice — and this
// one is what an operator is told to search for. A boundary that starts
// recording under a new name and does not say so on the screen is a trail
// nobody finds.
func TestTheDLPScreenNamesEveryActionTheTrailIsWrittenUnder(t *testing.T) {
	root := filepath.Join("..", "..")
	page, err := os.ReadFile(filepath.Join(root, "web", "src", "pages", "AdminDLP.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	shown := string(page)

	action := regexp.MustCompile(`"(dlp\.[a-z]+)"`)
	recorded := map[string]string{}
	walkErr := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range action.FindAllStringSubmatch(string(body), -1) {
			// dlp.update records a settings change rather than a finding: it is
			// what somebody configured, not what left the building.
			if match[1] == "dlp.update" {
				continue
			}
			recorded[match[1]] = path
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(recorded) < 5 {
		t.Fatalf("only %d finding actions found; this guard is reading the wrong thing", len(recorded))
	}
	for name, path := range recorded {
		if !strings.Contains(shown, name) {
			t.Errorf("%s writes findings under %s and the DLP screen never mentions it", path, name)
		}
	}
}
