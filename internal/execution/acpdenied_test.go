package execution

import (
	"os"
	"strings"
	"testing"
)

// An agent refused its tools often ends with nothing to say, and the platform is
// the one that refused them. Reporting only the silence sends somebody to look
// at the agent for a decision this deployment made.
//
// Measured on a cluster: a rejected write ended the run as "에이전트가 대화를
// 끝냈지만 답변이 비어 있습니다" with no mention of the refusal — while the
// refusal was this platform's own, one event earlier in the same run.
func TestAnEmptyAnswerAfterRefusalsSaysWhy(t *testing.T) {
	body, err := os.ReadFile("acp.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, `if strings.TrimSpace(answer) == ""`)
	if at < 0 {
		t.Fatal("the empty-answer path is gone; this guard is reading nothing")
	}
	section := source[at:]
	if end := strings.Index(section, "o.flowInspector"); end >= 0 {
		section = section[:end]
	}
	if !strings.Contains(section, "turn.denied > 0") {
		t.Error("an empty answer never mentions the tools this deployment refused")
	}
	if !strings.Contains(section, "turn.denied)") {
		t.Error("the report does not say how many were refused")
	}
	// A refusal is not a flake: the same run would ask the same question and get
	// the same answer, so retrying only spends the agent's time again.
	deniedAt := strings.Index(section, "turn.denied > 0")
	window := section[deniedAt:]
	// Clamped: the branch is a few lines, and a guard that panics on its own
	// arithmetic tells nobody anything about the code it guards.
	if len(window) > 400 {
		window = window[:400]
	}
	if !strings.Contains(window, "Retryable: false") {
		t.Error("a run stopped by a person's refusal is retried")
	}
	// And a silence with no refusals keeps the words it had — that one is worth
	// retrying, because it may be a flake.
	if !strings.Contains(section, `"에이전트가 대화를 끝냈지만 답변이 비어 있습니다."`) {
		t.Error("the ordinary empty answer lost its own message")
	}
}
