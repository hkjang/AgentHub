package execution

import (
	"context"
	"os"
	"strings"
	"testing"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// A real Langflow, reached the way a task reaches one.
//
// The reader in flow_test.go is pinned to a response that was captured once. This
// test is how that capture is kept honest: point it at a running runtime and it
// performs the same request the worker performs — through the token-checking proxy
// if one is in front — and asserts the answer comes back readable.
//
// Skipped without the two variables, because CI has no runtime:
//
//	AGENTHUB_LIVE_LANGFLOW=http://127.0.0.1:9119 \
//	AGENTHUB_LIVE_LANGFLOW_TOKEN=... \
//	AGENTHUB_LIVE_LANGFLOW_FLOW=<flow id> \
//	go test ./internal/execution -run TestLiveFlowRun -v
func TestLiveFlowRun(t *testing.T) {
	endpoint := os.Getenv("AGENTHUB_LIVE_LANGFLOW")
	token := os.Getenv("AGENTHUB_LIVE_LANGFLOW_TOKEN")
	flowID := os.Getenv("AGENTHUB_LIVE_LANGFLOW_FLOW")
	if endpoint == "" || token == "" || flowID == "" {
		t.Skip("set AGENTHUB_LIVE_LANGFLOW, AGENTHUB_LIVE_LANGFLOW_TOKEN and AGENTHUB_LIVE_LANGFLOW_FLOW to run this against a real runtime")
	}
	orchestrator := &Orchestrator{}
	connection := appRuntime.Connection{Endpoint: endpoint, Token: token, RuntimeType: runtimetype.Langflow}
	goal := store.AgentGoal{Runner: store.RunnerFlow, FlowID: flowID}
	task := store.AgentTask{ID: "live-task", Title: "라이브 확인", Input: "흐름이 응답하는지 확인합니다"}

	answer, usage, err := orchestrator.callFlow(context.Background(), connection, goal, task, runnerInput(task, goal))
	if err != nil {
		t.Fatalf("the flow did not run: %v", err)
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("the flow ran but returned nothing readable")
	}
	t.Logf("answer: %s", answer)
	t.Logf("runtime reported usage: %v", usage)

	// The wrong token has to be refused, and refused in a way the retry policy
	// treats as final: a rejected credential does not become valid on the next try.
	_, _, err = orchestrator.callFlow(context.Background(), appRuntime.Connection{Endpoint: endpoint, Token: token + "-wrong", RuntimeType: runtimetype.Langflow}, goal, task, "x")
	if err == nil {
		t.Fatal("a wrong runtime token was accepted")
	}
	if retryableFlowError(err) {
		t.Errorf("a rejected credential must not be retried: %v", err)
	}
	// And an unknown flow id is equally final.
	_, _, err = orchestrator.callFlow(context.Background(), connection, store.AgentGoal{FlowID: "00000000-0000-0000-0000-000000000000"}, task, "x")
	if err == nil {
		t.Fatal("an unknown flow id was accepted")
	}
	if retryableFlowError(err) {
		t.Errorf("an unknown flow must not be retried: %v", err)
	}
}
