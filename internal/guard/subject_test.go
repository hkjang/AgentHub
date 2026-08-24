package guard

import (
	"os"
	"strings"
	"testing"
)

// One inspector guards every backend the worker runs: cli, acp, rpc, orca,
// review and the agent server all pass their text through the same one. Naming
// it after the flow backend told somebody running an ACP agent that their
// "흐름 요청" had been blocked — a boundary they never used and a word they
// cannot act on.
//
// Measured: a card number in an ACP task was blocked with "흐름 요청에
// 민감정보가 포함되어 있습니다".
func TestTheBlockedMessageNamesSomethingThePersonUsed(t *testing.T) {
	body, err := os.ReadFile("guard.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func NewFlow(")
	if at < 0 {
		t.Fatal("the shared inspector's constructor is gone; this guard is reading nothing")
	}
	constructor := source[at:]
	if end := strings.Index(constructor, "\n}\n"); end >= 0 {
		constructor = constructor[:end]
	}
	if strings.Contains(constructor, `subject: "흐름"`) {
		t.Error("the inspector every backend shares still names itself after one of them")
	}
	if !strings.Contains(constructor, `subject: "에이전트"`) {
		t.Errorf("the shared inspector names its boundary something else: %q", constructor)
	}
	// The event type is history: past runs were recorded under it, and renaming
	// it would rewrite a timeline rather than fix a sentence.
	if !strings.Contains(constructor, `event: "dlp.flow"`) {
		t.Error("the event type changed, which renames what past runs were recorded as")
	}
}
