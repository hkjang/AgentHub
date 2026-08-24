package execution

import (
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
