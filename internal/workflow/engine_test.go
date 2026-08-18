package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder answers with a canned reply per step and records the prompt it saw.
type recorder struct {
	mu       sync.Mutex
	prompts  map[string]string
	systems  map[string]string
	replies  map[string]string
	failures map[string]error
	inFlight int32
	peak     int32
	delay    time.Duration
}

func newRecorder() *recorder {
	return &recorder{prompts: map[string]string{}, systems: map[string]string{}, replies: map[string]string{}, failures: map[string]error{}}
}

func (r *recorder) Complete(ctx context.Context, step Step, prompt string) (string, error) {
	current := atomic.AddInt32(&r.inFlight, 1)
	for {
		peak := atomic.LoadInt32(&r.peak)
		if current <= peak || atomic.CompareAndSwapInt32(&r.peak, peak, current) {
			break
		}
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			atomic.AddInt32(&r.inFlight, -1)
			return "", ctx.Err()
		}
	}
	atomic.AddInt32(&r.inFlight, -1)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts[step.ID] = prompt
	r.systems[step.ID] = step.SystemPrompt
	if err, failing := r.failures[step.ID]; failing {
		return "", err
	}
	if reply, ok := r.replies[step.ID]; ok {
		return reply, nil
	}
	return "output-" + step.ID, nil
}

func chain() []Step {
	return []Step{
		{ID: "a", AgentID: "agent-a", AgentName: "Analyst"},
		{ID: "b", AgentID: "agent-b", AgentName: "Writer", DependsOn: []string{"a"}},
		{ID: "c", AgentID: "agent-c", AgentName: "Reviewer", DependsOn: []string{"b"}},
	}
}

func TestSequentialRunFeedsEachStepItsUpstreamOutput(t *testing.T) {
	client := newRecorder()
	result, err := New(client).Run(context.Background(), "sequential", chain(), Guardrails{MaxParallel: 2}, "요약해 주세요")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.AgentCall != 3 {
		t.Fatalf("unexpected result: status=%s calls=%d", result.Status, result.AgentCall)
	}
	if !strings.Contains(client.prompts["a"], "요약해 주세요") {
		t.Fatalf("the run input never reached the entry step: %q", client.prompts["a"])
	}
	// The dependency's output must arrive attributed to the agent that produced it.
	if !strings.Contains(client.prompts["b"], "output-a") || !strings.Contains(client.prompts["b"], "Analyst") {
		t.Fatalf("step b did not receive step a's attributed output: %q", client.prompts["b"])
	}
	if result.Output != "output-c" {
		t.Fatalf("a chain must answer with its terminal step, got %q", result.Output)
	}
	if len(result.Levels) != 3 {
		t.Fatalf("expected three levels, got %#v", result.Levels)
	}
}

func TestParallelRunAggregatesEveryContribution(t *testing.T) {
	steps := []Step{
		{ID: "a", AgentName: "Alpha"},
		{ID: "b", AgentName: "Beta"},
		{ID: "c", AgentName: "Gamma"},
	}
	result, err := New(newRecorder()).Run(context.Background(), "parallel", steps, Guardrails{MaxParallel: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Alpha", "Beta", "Gamma"} {
		if !strings.Contains(result.Output, name) {
			t.Fatalf("parallel output is missing %s: %q", name, result.Output)
		}
	}
}

func TestMaxParallelIsEnforced(t *testing.T) {
	steps := []Step{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	client := newRecorder()
	client.delay = 40 * time.Millisecond
	if _, err := New(client).Run(context.Background(), "parallel", steps, Guardrails{MaxParallel: 2}, ""); err != nil {
		t.Fatal(err)
	}
	if peak := atomic.LoadInt32(&client.peak); peak > 2 {
		t.Fatalf("ran %d steps at once, the limit is 2", peak)
	}
}

func TestFailedStepStopsTheRunButKeepsTheTrace(t *testing.T) {
	client := newRecorder()
	client.failures["b"] = errors.New("gateway exploded")
	result, err := New(client).Run(context.Background(), "sequential", chain(), Guardrails{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("the partial trace must keep the steps that ran: %#v", result.Steps)
	}
	if result.Steps[1].Error == "" {
		t.Fatal("the failing step must record why it failed")
	}
	if _, ran := client.prompts["c"]; ran {
		t.Fatal("a step downstream of a failure must not run")
	}
}

func TestMaxAgentCallsStopsTheRun(t *testing.T) {
	result, err := New(newRecorder()).Run(context.Background(), "sequential", chain(), Guardrails{MaxAgentCalls: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Output, "최대 Agent 호출") {
		t.Fatalf("the call limit was not enforced: %#v", result)
	}
}

func TestMaxDepthIsRejectedBeforeAnyAgentRuns(t *testing.T) {
	client := newRecorder()
	if _, err := New(client).Run(context.Background(), "sequential", chain(), Guardrails{MaxDepth: 2}, ""); err == nil {
		t.Fatal("a graph deeper than the limit must be rejected")
	}
	if len(client.prompts) != 0 {
		t.Fatal("no agent may be called when the depth check fails")
	}
}

func TestRouterRunsOnlyTheBranchItChose(t *testing.T) {
	steps := []Step{
		{ID: "route", AgentName: "Router"},
		{ID: "billing", AgentName: "Billing", DependsOn: []string{"route"}},
		{ID: "security", AgentName: "Security", DependsOn: []string{"route"}},
	}
	client := newRecorder()
	client.replies["route"] = `{"branches":["security"],"reason":"계정 탈취 신고","handoff":"로그인 로그를 확인해 주세요"}`
	result, err := New(client).Run(context.Background(), "router", steps, Guardrails{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ran := client.prompts["security"]; !ran {
		t.Fatal("the chosen branch did not run")
	}
	if _, ran := client.prompts["billing"]; ran {
		t.Fatal("a branch that was not chosen must be skipped")
	}
	var skipped bool
	for _, item := range result.Steps {
		if item.ID == "billing" && item.Skipped {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("the skipped branch must be visible in the trace: %#v", result.Steps)
	}
	// The decision is on the record rather than inferred from what ran.
	if result.Routing == nil || len(result.Routing.Chosen) != 1 || result.Routing.Chosen[0] != "security" {
		t.Fatalf("unexpected routing record: %#v", result.Routing)
	}
	if result.Routing.FellBack || result.Routing.Reason != "계정 탈취 신고" {
		t.Fatalf("unexpected routing record: %#v", result.Routing)
	}
	// The branch is told what to do, not handed the decision JSON.
	if prompt := client.prompts["security"]; !strings.Contains(prompt, "로그인 로그를 확인해 주세요") {
		t.Fatalf("the handoff never reached the branch: %q", prompt)
	}
	if prompt := client.prompts["security"]; strings.Contains(prompt, "branches") {
		t.Fatalf("the branch was handed the raw decision: %q", prompt)
	}
}

func TestRouterProseNoLongerSelectsByMentioningAName(t *testing.T) {
	// The old reading looked for a branch's id or agent name anywhere in the
	// answer, so a sentence that ruled a branch out selected it. An answer that is
	// not a decision now runs the whole graph and says why, which is visible rather
	// than being mistaken for a choice somebody made.
	steps := []Step{
		{ID: "route", AgentName: "Router"},
		{ID: "billing", AgentName: "Billing", DependsOn: []string{"route"}},
		{ID: "security", AgentName: "Security", DependsOn: []string{"route"}},
	}
	client := newRecorder()
	client.replies["route"] = "이 건은 security 담당이 아니라 billing 쪽도 아닙니다"
	result, err := New(client).Run(context.Background(), "router", steps, Guardrails{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Routing == nil || !result.Routing.FellBack {
		t.Fatalf("an unreadable decision must be recorded as a fallback: %#v", result.Routing)
	}
	if result.Routing.Note == "" {
		t.Fatal("the fallback must say what went wrong")
	}
	for _, id := range []string{"billing", "security"} {
		if _, ran := client.prompts[id]; !ran {
			t.Fatalf("the fallback must run every branch, %s did not run", id)
		}
	}
}

func TestRouterIgnoresBranchesThatDoNotExist(t *testing.T) {
	steps := []Step{
		{ID: "route", AgentName: "Router"},
		{ID: "billing", AgentName: "Billing", DependsOn: []string{"route"}},
	}
	client := newRecorder()
	client.replies["route"] = `{"branches":["legal","../billing"],"reason":"x","handoff":"y"}`
	result, err := New(client).Run(context.Background(), "router", steps, Guardrails{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Routing == nil || !result.Routing.FellBack {
		t.Fatalf("a decision naming nothing real must not be treated as a choice: %#v", result.Routing)
	}
}

func TestRouterIsToldWhichBranchesExist(t *testing.T) {
	steps := []Step{
		{ID: "route", AgentName: "Router"},
		{ID: "billing", AgentName: "Billing", DependsOn: []string{"route"}},
		{ID: "security", AgentName: "Security", DependsOn: []string{"route"}},
	}
	client := newRecorder()
	client.replies["route"] = `{"branches":["billing"],"reason":"결제 중복","handoff":"청구 내역 확인"}`
	if _, err := New(client).Run(context.Background(), "router", steps, Guardrails{}, ""); err != nil {
		t.Fatal(err)
	}
	// A constrained answer is only possible if the router knows which ids exist:
	// the enum in the schema and the list in the prompt come from the same place.
	instruction := client.systems["route"]
	for _, want := range []string{"billing", "security", "branches"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("the router was not told about %q: %q", want, instruction)
		}
	}
	if strings.Contains(client.systems["billing"], "branches") {
		t.Fatal("a branch must not be given the router's instruction")
	}
}

func TestCyclesAndDanglingDependenciesAreRejected(t *testing.T) {
	cyclic := []Step{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}}
	if _, err := New(newRecorder()).Run(context.Background(), "sequential", cyclic, Guardrails{}, ""); err == nil {
		t.Fatal("a cycle must be rejected")
	}
	dangling := []Step{{ID: "a", DependsOn: []string{"ghost"}}}
	if _, err := New(newRecorder()).Run(context.Background(), "sequential", dangling, Guardrails{}, ""); err == nil {
		t.Fatal("an unknown dependency must be rejected")
	}
	duplicate := []Step{{ID: "a"}, {ID: "a"}}
	if _, err := New(newRecorder()).Run(context.Background(), "sequential", duplicate, Guardrails{}, ""); err == nil {
		t.Fatal("duplicate step ids must be rejected")
	}
	if _, err := New(newRecorder()).Run(context.Background(), "sequential", nil, Guardrails{}, ""); !errors.Is(err, ErrNoSteps) {
		t.Fatal("an empty graph must be rejected")
	}
}

func TestOutputIsTruncatedToTheConfiguredLimit(t *testing.T) {
	client := newRecorder()
	client.replies["a"] = strings.Repeat("가", 100)
	result, err := New(client).Run(context.Background(), "parallel", []Step{{ID: "a", AgentName: "A"}}, Guardrails{MaxOutputRunes: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Steps[0].Output, "생략됨") {
		t.Fatalf("a long output must be truncated: %q", result.Steps[0].Output)
	}
}

func TestRunHonoursTheDurationGuardrail(t *testing.T) {
	client := newRecorder()
	client.delay = 200 * time.Millisecond
	result, err := New(client).Run(context.Background(), "sequential", chain(), Guardrails{MaxDuration: 50 * time.Millisecond}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("a run past its deadline must fail, got %s", result.Status)
	}
}
