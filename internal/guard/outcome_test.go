package guard

import (
	"os"
	"strings"
	"testing"
)

// The audit trail has to say what happened to the text.
//
// This boundary recorded "redacted" for anything it found and did not block,
// and 기록만 is the documented way a site learns what its agents actually handle
// before it starts blocking anything. So the deployments following that advice
// were the ones whose trail claimed, of every model call, that the platform had
// rewritten a prompt it had passed through untouched — and that trail is what
// somebody reads to decide whether to start redacting.
//
// The check reads the source because record() writes to the database, and a
// boundary that needs PostgreSQL to prove it tells the truth is one nobody runs.
func TestTheTrailSaysWhatWasDoneToTheText(t *testing.T) {
	body, err := os.ReadFile("guard.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (m *Model) record(")
	if at < 0 {
		t.Fatal("the recorder is gone; this guard is reading nothing")
	}
	recorder := source[at:]
	if end := strings.Index(recorder, "\n}\n"); end >= 0 {
		recorder = recorder[:end]
	}
	if strings.Contains(recorder, `outcome := "redacted"`) {
		t.Error("every finding is still recorded as a redaction, including the ones that only got recorded")
	}
	if !strings.Contains(recorder, "result.Outcome()") {
		t.Errorf("the outcome is not taken from what the scan did: %q", recorder)
	}
	// A policy rule can refuse text the scanner itself only recorded, and that
	// refusal is what happened to the call.
	if !strings.Contains(recorder, "dlp.OutcomeBlocked") {
		t.Error("a policy block no longer overrides the scanner's own outcome")
	}
}
