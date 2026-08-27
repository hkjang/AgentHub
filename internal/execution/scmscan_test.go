package execution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/store"
)

// The review comment is the fifth way text leaves this deployment, and it was
// the one nobody inspected: a model writes it, quoting somebody's code, and it
// goes to a host this deployment does not own. A live run proved it — a review
// finding carrying a national ID reached a forge while the deployment's scanner
// was set to block national IDs, because nothing on that path ever asked.

func blockingRRN() dlp.Settings {
	return dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Block}}
}

func redactingRRN() dlp.Settings {
	return dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Redact}}
}

// forgeRecorder answers like a forge that holds no comment yet and remembers
// every body it was handed.
func forgeRecorder(t *testing.T, bodies *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, string(raw))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
}

func TestAReviewCommentIsScannedOnItsWayOut(t *testing.T) {
	var bodies []string
	forge := forgeRecorder(t, &bodies)
	defer forge.Close()
	connection := store.SCMConnection{
		Host:    strings.TrimPrefix(forge.URL, "http://"),
		Kind:    "gitea",
		APIBase: forge.URL + "/api/v1",
	}
	comment := ReviewComment("픽스처 1건", []store.ReviewFinding{{
		Severity: "high", FilePath: "app/pay.py", StartLine: 5,
		Message: "테스트 픽스처에 실제 주민등록번호 900101-1234568 가 그대로 들어 있습니다.",
	}}, 10)
	source := forge.URL + "/acme/store/pulls/9"

	err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret", source, comment, blockingRRN())
	var withheld WithheldError
	if !asWithheld(err, &withheld) {
		t.Fatalf("a blocked comment was not refused: %v", err)
	}
	if len(withheld.Classes) == 0 || withheld.Classes[0] != "rrn" {
		t.Errorf("the refusal does not say what it found: %v", withheld.Classes)
	}
	if len(bodies) != 0 {
		t.Fatalf("a blocked comment reached the forge anyway: %q", bodies)
	}

	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret", source, comment, redactingRRN()); err != nil {
		t.Fatalf("a redactable comment was not posted: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("the redacted comment did not reach the forge: %q", bodies)
	}
	var posted struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &posted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(posted.Body, "900101-1234568") {
		t.Fatalf("the national id was posted in the clear: %q", posted.Body)
	}
	if !strings.Contains(posted.Body, "app/pay.py:5") {
		t.Fatalf("redaction took the whole finding with it: %q", posted.Body)
	}

	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret", source, comment, dlp.Settings{}); err != nil {
		t.Fatalf("an unconfigured deployment could not post: %v", err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "900101-1234568") {
		t.Fatalf("a deployment that scans nothing had its comment changed: %q", bodies)
	}
}

// asWithheld is errors.As without the import, so this file reads as one thing.
func asWithheld(err error, target *WithheldError) bool {
	withheld, ok := err.(WithheldError)
	if ok {
		*target = withheld
	}
	return ok
}

// TestTheScanIsInsideTheSend fails if the inspection is moved back out to the
// caller. That is not style: the same mistake was made once already on the
// decision export, where a caller scanned and then sent the original.
func TestTheScanIsInsideTheSend(t *testing.T) {
	source := readSource(t, "scmpost.go")
	at := strings.Index(source, "func PostReviewComment(")
	if at < 0 {
		t.Fatal("the sender is gone; this guard is reading nothing")
	}
	body := source[at:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "dlp.Scan(") {
		t.Error("PostReviewComment sends text it never scanned")
	}
	scanned := strings.Index(body, "dlp.Scan(")
	if request := strings.Index(body, "commentRequest("); request < scanned {
		t.Error("the request is built before the scan, so blocked text is already addressed")
	}
	if !strings.Contains(body, "text = scanned.Text") {
		t.Error("the scan's result is discarded; the original text is what gets sent")
	}
}

// TestAWithheldReviewIsLoudAndNotBlamedOnTheForge fails if a comment held back
// by the scanner is silent, or is recorded as the forge refusing it. A review
// that posts nothing looks exactly like a clean review from the pull request.
func TestAWithheldReviewIsLoudAndNotBlamedOnTheForge(t *testing.T) {
	source := readSource(t, "scmpost.go")
	at := strings.Index(source, "func (o *Orchestrator) announceReview(")
	if at < 0 {
		t.Fatal("the announcer is gone; this guard is reading nothing")
	}
	body := source[at:]
	if end := strings.Index(body, "\n// scmHTTPClient"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "errors.As(err, &withheld)") {
		t.Error("a withheld review is not told apart from a forge that refused")
	}
	if !strings.Contains(body, "review.withheld") {
		t.Error("a withheld review writes no event, so the run cannot say why it posted nothing")
	}
	if !strings.Contains(body, "o.logger.Warn") {
		t.Error("a withheld review is not logged")
	}
	withheldAt := strings.Index(body, "errors.As(err, &withheld)")
	recordAt := strings.Index(body, "RecordSCMUse(")
	if withheldAt > 0 && recordAt > 0 && !strings.Contains(body[withheldAt:recordAt], "return") {
		t.Error("a withheld review falls through and marks the connection as having failed")
	}
}

// TestTheSettingsAreReadFromTheDeployment fails if the announcer passes a fixed
// or empty policy: the screen an administrator configures has to be the one the
// review obeys.
func TestTheSettingsAreReadFromTheDeployment(t *testing.T) {
	source := readSource(t, "scmpost.go")
	at := strings.Index(source, "func (o *Orchestrator) announceReview(")
	if at < 0 {
		t.Fatal("the announcer is gone; this guard is reading nothing")
	}
	body := source[at:]
	if end := strings.Index(body, "\n// scmHTTPClient"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "o.contentSettings(ctx)") {
		t.Error("announceReview does not read this deployment's content settings")
	}
	if !strings.Contains(source, "o.store.Setting(ctx, dlp.SettingKey") {
		t.Error("the settings are not read from where the console writes them")
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("the guard cannot read %s: %v", name, err)
	}
	return string(raw)
}
