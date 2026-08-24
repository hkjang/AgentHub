package execution

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Content inspection blocked what an agent said from being used, and every
// backend had already written the raw text into the run's step: the card number
// the scanner refused to pass on sat in this platform's own database, on the
// run's timeline, for anybody who could open it.
//
// Measured on a cluster: a model answering with a card number produced a failed
// task and a step whose output was the card number.
func TestAnAnswerIsScannedBeforeItIsStored(t *testing.T) {
	// What the step records must be what the scanner returned, named here per
	// backend because each spells its own answer differently. Comparing the
	// position of the first AppendRunStep would compare against a failure path
	// that records no answer at all — the first version of this guard did, and
	// flagged two backends that were already right.
	for file, expected := range map[string][2]string{
		"acp.go":         {"Output: inspected,", "Output: turn.answer(),"},
		"cli.go":         {"record.Output = inspected", "record.Output = parsed.Result"},
		"rpc.go":         {"Output:       inspected,", "Output:       result.Answer,"},
		"agentserver.go": {"Output: inspected,", "Output: result.Answer,"},
		"investigate.go": {"record.Output = inspected", "record.Output = report.Conclusion"},
		// flow and dify are why the inspector exists, and they were the two this
		// guard did not name — so the fix that closed every other backend left
		// them open and the guard reported success.
		"flow.go": {"Output: inspected, Status:", "Output: answer, Status:"},
		"dify.go": {"Output: inspected, Status:", "Output: answer, Status:"},
		// The fabric's summary quotes each worker's own last words, and orca had
		// no inbound scan at all — only its prompt was ever read.
		"orca.go": {"record.Output = inspected", "record.Output = summary"},
	} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.Contains(source, "o.inspectAnswer(ctx, step,") {
			t.Errorf("%s never scans its answer", file)
			continue
		}
		if !strings.Contains(source, expected[0]) {
			t.Errorf("%s does not record the scanned text (%q)", file, expected[0])
		}
		if strings.Contains(source, expected[1]) {
			t.Errorf("%s records the agent's own text (%q) — the scanner sees it afterwards", file, expected[1])
		}
	}
}

// The review backend has had a scanner since the day it was written — its own
// helper, with a comment about findings leaving the platform — and nothing
// called it. Since the review now comments on the pull request it came from,
// that text leaves this deployment for a forge.
func TestReviewFindingsAreScannedBeforeTheyAreStored(t *testing.T) {
	body, err := os.ReadFile("review.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	uses := strings.Count(source, "o.reviewInspection(ctx, step,")
	if uses == 0 {
		t.Fatal("the review's own content scanner is defined and never called")
	}
	scan := strings.Index(source, "o.reviewInspection(ctx, step,")
	save := strings.Index(source, "SaveReviewFindings(")
	if save < 0 {
		t.Fatal("the findings are no longer saved; this guard is reading nothing")
	}
	if scan > save {
		t.Error("the findings are stored before they are scanned")
	}
	// Field by field: a redaction applied to one joined string cannot be split
	// back into the fields it came from.
	if !strings.Contains(source, "findings[index].Message = message") {
		t.Error("a finding's own text is not what the scanner returned")
	}
}

// A blocked answer is empty by construction. Checking the silence before the
// refusal hides the reason and retries a decision that cannot change — which is
// what the first version of this fix did: the run said "답변이 비어 있습니다"
// about text the scanner had just refused.
func TestABlockedAnswerIsNotReportedAsSilence(t *testing.T) {
	body, err := os.ReadFile("acp.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	refusal := strings.Index(source, "Failure: inspectErr.Error()")
	silence := strings.Index(source, `if strings.TrimSpace(answer) == ""`)
	if refusal < 0 || silence < 0 {
		t.Fatal("the answer paths are gone; this guard is reading nothing")
	}
	if refusal > silence {
		t.Error("an answer the scanner refused is reported as an agent that said nothing")
	}
}

// A refusal survives a failure that happened at the same moment.
//
// Every backend that scans can also fail for its own reason — a timeout, a
// cancellation, an answer it could not parse — and the step recorded whichever
// error was assigned last. The output was gone either way, so an empty step
// explained by a timeout read as a run that produced nothing rather than one
// whose product the platform refused to keep.
//
// Measured live on the fabric backend before this was shared: a run cancelled
// while a card number sat in its workers' words stored an empty step and
// "워커 실행이 취소됐습니다", with no mention of the refusal.
func TestBothReasonsSurviveWhenBothHappened(t *testing.T) {
	if got := failureWith(errors.New("취소됐습니다"), errors.New("차단되었습니다")); got != "취소됐습니다 — 차단되었습니다" {
		t.Errorf("both reasons read as %q", got)
	}
	if got := failureWith(nil, errors.New("차단되었습니다")); got != "차단되었습니다" {
		t.Errorf("a refusal alone reads as %q", got)
	}
	if got := failureWith(errors.New("취소됐습니다"), nil); got != "취소됐습니다" {
		t.Errorf("a failure alone reads as %q", got)
	}
	if got := failureWith(nil, nil); got != "" {
		t.Errorf("nothing wrong reads as %q", got)
	}
	for _, file := range []string{"acp.go", "cli.go", "rpc.go", "agentserver.go", "investigate.go", "flow.go", "dify.go", "orca.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.Contains(source, "o.inspectAnswer(ctx, step,") {
			continue
		}
		if !strings.Contains(source, "failureWith(") {
			t.Errorf("%s can lose a refusal to whichever error is assigned after it", file)
		}
	}
}

// A step whose answer the scanner refused is not a step that succeeded.
//
// The cli backend cleared the refused answer and left the status alone, so a
// blocked run showed a step marked succeeded with nothing in it — the shape
// this platform keeps removing, a failure wearing a healthy word.
func TestARefusedStepIsNotASucceededStep(t *testing.T) {
	for _, file := range []string{"acp.go", "cli.go", "rpc.go", "agentserver.go", "investigate.go", "flow.go", "dify.go", "orca.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.Contains(source, "o.inspectAnswer(ctx, step,") {
			continue
		}
		// That the scanned answer is what gets recorded is held by the guard
		// above; what this one holds is the status that goes with it.
		// Every backend must mark the step failed somewhere that inspectErr can
		// reach — either on its own or together with another reason.
		if !strings.Contains(source, "failureWith(") && !strings.Contains(source, `record.Status, record.Error, record.Output = "failed", inspectErr.Error(), ""`) {
			t.Errorf("%s can leave a refused step marked succeeded", file)
		}
	}
}
