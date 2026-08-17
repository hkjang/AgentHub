package workflow

import (
	"context"
	"strings"
	"testing"
)

// scriptedRounds answers differently on each call to a step, so a review loop
// can be set up: a specialist that improves, a supervisor that changes its mind.
type scriptedRounds struct {
	byStep  map[string][]string
	calls   map[string]int
	prompts map[string][]string
	systems map[string]string
}

func (s *scriptedRounds) Complete(_ context.Context, step Step, prompt string) (string, error) {
	if s.calls == nil {
		s.calls = map[string]int{}
		s.prompts = map[string][]string{}
		s.systems = map[string]string{}
	}
	index := s.calls[step.ID]
	s.calls[step.ID]++
	s.prompts[step.ID] = append(s.prompts[step.ID], prompt)
	s.systems[step.ID] = step.SystemPrompt
	answers := s.byStep[step.ID]
	if len(answers) == 0 {
		return "", nil
	}
	if index >= len(answers) {
		index = len(answers) - 1
	}
	return answers[index], nil
}

// A chain of two specialists feeding one reviewer.
func supervisedSteps() []Step {
	return []Step{
		{ID: "a", AgentID: "agent-a", AgentName: "분석가", SystemPrompt: "분석합니다."},
		{ID: "b", AgentID: "agent-b", AgentName: "작성자", SystemPrompt: "작성합니다."},
		{ID: "s", AgentID: "agent-s", AgentName: "감독자", SystemPrompt: "감독합니다.", DependsOn: []string{"a", "b"}},
	}
}

func TestSupervisorApprovalIsRecorded(t *testing.T) {
	script := &scriptedRounds{byStep: map[string][]string{
		"a": {"분석 결과"}, "b": {"보고서 초안"},
		"s": {"두 결과 모두 요구사항을 충족합니다.\nAPPROVE"},
	}}
	result, err := New(script).Run(context.Background(), "supervisor", supervisedSteps(), Guardrails{}, "보고서를 작성하라")
	if err != nil {
		t.Fatal(err)
	}
	if result.Supervision == nil || !result.Supervision.Approved {
		t.Fatalf("supervision = %#v, want an approval", result.Supervision)
	}
	if !strings.Contains(script.systems["s"], "REVISE:") {
		t.Fatalf("the supervisor was never told how to ask for changes: %q", script.systems["s"])
	}
	// The specialists must not be told to supervise.
	if strings.Contains(script.systems["a"], "REVISE:") {
		t.Fatalf("a specialist got the supervisor's instruction: %q", script.systems["a"])
	}
	if script.calls["a"] != 1 || script.calls["s"] != 1 {
		t.Fatalf("an approved review must not re-run anything: %#v", script.calls)
	}
	if !strings.Contains(result.Output, "승인") {
		t.Fatalf("the verdict should lead the output:\n%s", result.Output)
	}
}

// The point of the mode: a supervisor that finds a gap can have it fixed.
func TestSupervisorSendsWorkBackAndAcceptsTheRevision(t *testing.T) {
	script := &scriptedRounds{byStep: map[string][]string{
		"a": {"분석 결과"},
		"b": {"근거가 없는 초안", "근거를 붙인 개정 초안"},
		"s": {"REVISE: 작성자 — 수치 근거를 추가하세요", "이제 충분합니다.\nAPPROVE"},
	}}
	result, _ := New(script).Run(context.Background(), "supervisor", supervisedSteps(), Guardrails{}, "보고서를 작성하라")

	if script.calls["b"] != 2 {
		t.Fatalf("the named specialist should have run twice, ran %d", script.calls["b"])
	}
	if script.calls["a"] != 1 {
		t.Fatalf("a specialist nobody asked about must not re-run, ran %d", script.calls["a"])
	}
	if script.calls["s"] != 2 {
		t.Fatalf("the supervisor should review the revision, ran %d", script.calls["s"])
	}
	// The specialist has to be told what to change, or it just answers again.
	second := script.prompts["b"][1]
	if !strings.Contains(second, "수치 근거") {
		t.Fatalf("the revision request did not reach the specialist:\n%s", second)
	}
	if result.Supervision == nil || !result.Supervision.Approved || result.Supervision.Exhausted {
		t.Fatalf("supervision = %#v, want an approval after one revision", result.Supervision)
	}
	// The trace must show the revised answer, not the one that was rejected.
	if !strings.Contains(result.Output, "개정 초안") || strings.Contains(result.Output, "근거가 없는 초안") {
		t.Fatalf("output should carry the revised answer:\n%s", result.Output)
	}
}

// A supervisor and a specialist that disagree will disagree indefinitely; the
// run has to end and say that it did.
func TestUnresolvedDisagreementEndsAsUnapproved(t *testing.T) {
	script := &scriptedRounds{byStep: map[string][]string{
		"a": {"분석 결과"}, "b": {"초안", "여전히 부족한 초안", "여전히 부족한 초안"},
		"s": {"REVISE: 작성자 — 더 구체적으로", "REVISE: 작성자 — 아직도 부족합니다", "REVISE: 작성자 — 여전히 부족합니다"},
	}}
	result, _ := New(script).Run(context.Background(), "supervisor", supervisedSteps(), Guardrails{}, "보고서")
	if result.Supervision.Approved {
		t.Fatal("a run that never got an approval must not report one")
	}
	if !result.Supervision.Exhausted {
		t.Fatalf("supervision = %#v, want it marked exhausted", result.Supervision)
	}
	if script.calls["s"] > maxRevisionRounds+1 {
		t.Fatalf("the review loop is unbounded: supervisor ran %d times", script.calls["s"])
	}
	if !strings.Contains(result.Output, "보완 요청이 남은") {
		t.Fatalf("the output should say the review did not conclude:\n%s", result.Output)
	}
}

// "APPROVE 할 수 없습니다" is a refusal. Substring matching would read it as
// approval and put the supervisor's name on work it rejected.
func TestApprovalMustBeTheWholeLine(t *testing.T) {
	byName := map[string]string{"작성자": "b"}
	for _, output := range []string{
		"이대로는 APPROVE 할 수 없습니다",
		"APPROVE 여부는 다음 검토에서 정합니다",
	} {
		if approved, _ := parseSupervision(output, byName); approved {
			t.Errorf("%q must not count as an approval", output)
		}
	}
	for _, output := range []string{"APPROVE", "  approve  ", "검토 완료\n**APPROVE**"} {
		if approved, _ := parseSupervision(output, byName); !approved {
			t.Errorf("%q should be an approval", output)
		}
	}
}

// A request for changes overrides an approval said in the same breath.
func TestRevisionOverridesApproval(t *testing.T) {
	byName := map[string]string{"작성자": "b"}
	approved, revisions := parseSupervision("APPROVE\nREVISE: 작성자 — 한 군데만 고쳐주세요", byName)
	if approved {
		t.Fatal("a review that still asks for changes has not approved")
	}
	if len(revisions) != 1 || revisions[0].StepID != "b" {
		t.Fatalf("revisions = %#v", revisions)
	}
}

func TestRevisionParsingToleratesHowModelsWrite(t *testing.T) {
	byName := map[string]string{"작성자": "b", "b": "b"}
	for _, output := range []string{
		"REVISE: 작성자 — 근거 추가",
		"REVISE: 작성자 - 근거 추가",
		"revise: 작성자: 근거 추가",
		"- REVISE: **작성자** — 근거 추가",
	} {
		_, revisions := parseSupervision(output, byName)
		if len(revisions) != 1 || revisions[0].StepID != "b" {
			t.Errorf("parseSupervision(%q) = %#v", output, revisions)
		}
	}
	// A name nobody answers to cannot be actioned; guessing would send the
	// wrong specialist back to work.
	if _, revisions := parseSupervision("REVISE: 존재하지 않는 담당자 — 고치세요", byName); len(revisions) != 0 {
		t.Errorf("an unknown agent must be ignored, got %#v", revisions)
	}
}

// Supervision needs one step with every answer in front of it. Promoting one of
// two terminals would nominate an agent the operator never chose.
func TestNoSupervisorWhenTheGraphHasNoSingleReviewer(t *testing.T) {
	parallel := []Step{
		{ID: "a", AgentName: "분석가"},
		{ID: "b", AgentName: "작성자"},
	}
	if got := supervisorStep("supervisor", parallel); got != nil {
		t.Fatalf("two terminals must not produce a supervisor, got %q", got.ID)
	}
	if got := supervisorStep("supervisor", []Step{{ID: "only"}}); got != nil {
		t.Fatal("a single step supervises nothing")
	}
	if got := supervisorStep("sequential", supervisedSteps()); got != nil {
		t.Fatal("only supervisor and reviewer modes supervise")
	}
	if got := supervisorStep("reviewer", supervisedSteps()); got == nil || got.ID != "s" {
		t.Fatalf("reviewer mode should supervise from the terminal, got %#v", got)
	}
}

// A run that cannot supervise must still produce what it always did.
func TestUnsupervisedGraphFallsBackToTheOldComposition(t *testing.T) {
	script := &scriptedRounds{byStep: map[string][]string{"a": {"분석"}, "b": {"작성"}}}
	steps := []Step{{ID: "a", AgentName: "분석가"}, {ID: "b", AgentName: "작성자"}}
	result, err := New(script).Run(context.Background(), "supervisor", steps, Guardrails{}, "질문")
	if err != nil {
		t.Fatal(err)
	}
	if result.Supervision != nil {
		t.Fatalf("no reviewer means no supervision record: %#v", result.Supervision)
	}
	for _, want := range []string{"분석", "작성"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("every contribution should still be reported:\n%s", result.Output)
		}
	}
}

// The call budget covers the review loop too, or a supervised run could quietly
// spend several times what the operator allowed.
func TestReviewLoopRespectsTheCallBudget(t *testing.T) {
	script := &scriptedRounds{byStep: map[string][]string{
		"a": {"분석"}, "b": {"초안", "개정"},
		"s": {"REVISE: 작성자 — 고치세요", "APPROVE"},
	}}
	// Three calls is exactly the first pass, leaving nothing for a revision.
	result, _ := New(script).Run(context.Background(), "supervisor", supervisedSteps(), Guardrails{MaxAgentCalls: 3}, "질문")
	if script.calls["b"] != 1 {
		t.Fatalf("the revision must not run past the budget, specialist ran %d", script.calls["b"])
	}
	if result.Supervision == nil || !result.Supervision.Exhausted {
		t.Fatalf("supervision = %#v, want it marked exhausted", result.Supervision)
	}
}
