package execution

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// The body below is a real answer from Langflow 1.11, trimmed to the fields the
// platform reads. It is here rather than hand-written because the shape is the
// runtime's, not ours: the same text appears in four places and a reader built
// from the documentation alone would have picked the wrong one.
const realFlowResponse = `{
  "session_id": "task-1",
  "outputs": [{
    "inputs": {"input_value": "이 작업을 처리해 주세요"},
    "outputs": [{
      "results": {"message": {"text_key": "text", "text": "처리했습니다", "session_id": "task-1"}},
      "artifacts": {"message": "처리했습니다", "sender": "Machine", "type": "object"},
      "outputs": {"message": {"message": "처리했습니다", "type": "text"}},
      "logs": {"message": []},
      "messages": [{"message": "처리했습니다", "sender": "Machine", "component_id": "ChatOutput-yK0AU", "type": "text"}],
      "component_display_name": "Chat Output",
      "component_id": "ChatOutput-yK0AU",
      "used_frozen_result": false,
      "token_usage": null
    }]
  }]
}`

func TestFlowAnswerReadsARealResponse(t *testing.T) {
	text, usage, ok := flowAnswer([]byte(realFlowResponse), "")
	if !ok || text != "처리했습니다" {
		t.Fatalf("flowAnswer() = %q, %v", text, ok)
	}
	if usage != nil {
		t.Errorf("a flow that reported no usage must not produce one: %v", usage)
	}
}

// A flow with two outputs is why the Goal can name one. Without it the platform
// would pick whichever ran last, which is not necessarily the answer.
func TestFlowAnswerHonoursTheChosenComponent(t *testing.T) {
	body := `{"outputs":[{"outputs":[
		{"component_id":"ChatOutput-A","results":{"message":{"text":"첫 번째"}}},
		{"component_id":"ChatOutput-B","results":{"message":{"text":"두 번째"}}}]}]}`
	if text, _, _ := flowAnswer([]byte(body), "ChatOutput-A"); text != "첫 번째" {
		t.Errorf("named component ignored, got %q", text)
	}
	// Unnamed takes the last one that produced text, which is the graph's own order.
	if text, _, _ := flowAnswer([]byte(body), ""); text != "두 번째" {
		t.Errorf("unnamed selection = %q, want the last output", text)
	}
	// A component that is not in the answer is a mistake worth reporting rather
	// than silently falling back to another output.
	if _, _, ok := flowAnswer([]byte(body), "ChatOutput-Z"); ok {
		t.Error("a missing output component must not resolve to another output")
	}
}

// The text is reported in several places depending on the component, and empty
// strings are common in the ones a component did not fill in.
func TestFlowAnswerFallsBackThroughTheReportedPlaces(t *testing.T) {
	cases := map[string]string{
		`{"outputs":[{"outputs":[{"outputs":{"message":{"message":"outputs 경로"}}}]}]}`:                      "outputs 경로",
		`{"outputs":[{"outputs":[{"artifacts":{"message":"artifacts 경로"}}]}]}`:                              "artifacts 경로",
		`{"outputs":[{"outputs":[{"messages":[{"message":"messages 경로"}]}]}]}`:                              "messages 경로",
		`{"outputs":[{"outputs":[{"results":{"message":{"text":"   "}},"artifacts":{"message":"공백 뒤"}}]}]}`: "공백 뒤",
	}
	for body, want := range cases {
		if text, _, ok := flowAnswer([]byte(body), ""); !ok || text != want {
			t.Errorf("body %s produced %q (ok=%v), want %q", body, text, ok, want)
		}
	}
	// A run with no readable output is a flow without an output component, which
	// the caller reports as such instead of recording an empty result as success.
	if _, _, ok := flowAnswer([]byte(`{"outputs":[{"outputs":[{"logs":{"message":[]}}]}]}`), ""); ok {
		t.Error("an empty flow answer must not be accepted")
	}
	if _, _, ok := flowAnswer([]byte("not json"), ""); ok {
		t.Error("an unparseable answer must not be accepted")
	}
}

// Token usage is passed through exactly as the runtime reported it, because the
// platform does not meter what happens inside a flow and must not invent a number.
func TestFlowAnswerPassesRuntimeUsageThrough(t *testing.T) {
	body := `{"outputs":[{"outputs":[{"results":{"message":{"text":"답"}},"token_usage":{"total_tokens":41,"model":"fake-model"}}]}]}`
	_, usage, ok := flowAnswer([]byte(body), "")
	if !ok || usage["total_tokens"] != float64(41) || usage["model"] != "fake-model" {
		t.Fatalf("usage = %#v", usage)
	}
}

// Retrying a rejected flow id spends the retry budget arriving at the same answer;
// retrying a runtime that was restarting is exactly what the budget is for.
func TestRetryableFlowError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{flowError{status: http.StatusNotFound, message: "unknown flow"}, false},
		{flowError{status: http.StatusForbidden, message: "no key"}, false},
		{flowError{status: http.StatusTooManyRequests, message: "busy"}, true},
		{flowError{status: http.StatusBadGateway, message: "restarting"}, true},
		{errors.New("connection refused"), true},
		{workflow.ErrBlocked, false},
	}
	for _, item := range cases {
		if got := retryableFlowError(item.err); got != item.want {
			t.Errorf("retryableFlowError(%v) = %v, want %v", item.err, got, item.want)
		}
	}
}

// What the flow receives has to carry the task and the standing instructions: a
// flow author cannot read the Goal, so anything not in the input does not exist.
func TestFlowInputCarriesTheTaskAndTheGoal(t *testing.T) {
	input := flowInput(
		store.AgentTask{Title: "월간 보고서 작성", Input: "8월 데이터"},
		store.AgentGoal{Description: "보고서를 만들고 저장한다", Constraints: "운영 DB 쓰기 금지"},
	)
	for _, want := range []string{"월간 보고서 작성", "8월 데이터", "보고서를 만들고 저장한다", "운영 DB 쓰기 금지"} {
		if !strings.Contains(input, want) {
			t.Errorf("flow input is missing %q:\n%s", want, input)
		}
	}
}

// A flow-backed Goal with no runtime fails as infrastructure, not as the agent's
// fault: the flow is in the Pod and there is no Pod, so a retry is worth having.
func TestRunFlowWithoutARuntimeIsRetryable(t *testing.T) {
	orchestrator := &Orchestrator{}
	_, outcome := orchestrator.runFlow(context.Background(), &store.AgentRun{}, store.AgentTask{}, store.Agent{}, store.AgentGoal{Runner: store.RunnerFlow}, nil)
	if outcome.Status != store.TaskFailed || !outcome.Retryable {
		t.Fatalf("outcome = %#v", outcome)
	}
	if !strings.Contains(outcome.Failure, "Runtime") {
		t.Errorf("the failure should say what is missing: %q", outcome.Failure)
	}
}

// blockingInspector refuses everything, the way the content scanner refuses a
// task carrying a resident registration number.
type blockingInspector struct{ calls int }

func (b *blockingInspector) Outbound(context.Context, workflow.Step, string) (string, error) {
	b.calls++
	return "", fmt.Errorf("%w: 주민등록번호가 포함되어 있습니다", workflow.ErrBlocked)
}
func (b *blockingInspector) Inbound(_ context.Context, _ workflow.Step, text string) (string, error) {
	return text, nil
}

// A refused task must not reach the runtime at all, and must not be retried: the
// same task carries the same data and would be refused again, spending the whole
// retry budget to arrive at the same answer.
func TestRunFlowRefusesBeforeTouchingTheRuntime(t *testing.T) {
	inspector := &blockingInspector{}
	orchestrator := (&Orchestrator{}).WithFlowInspector(inspector)
	_, outcome := orchestrator.runFlow(context.Background(), &store.AgentRun{}, store.AgentTask{Title: "고객 정리"}, store.Agent{},
		store.AgentGoal{Runner: store.RunnerFlow, FlowID: "flow-1"}, &acquiredRuntime{runtimeID: "rt-1"})
	if outcome.Status != store.TaskFailed || outcome.Retryable {
		t.Fatalf("outcome = %#v", outcome)
	}
	if inspector.calls != 1 {
		t.Errorf("the inspector ran %d times", inspector.calls)
	}
	if !strings.Contains(outcome.Failure, "주민등록번호") {
		t.Errorf("the refusal should say why: %q", outcome.Failure)
	}
}
