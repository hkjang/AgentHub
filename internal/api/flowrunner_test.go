package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// Which runner a Goal may use is a fact about the agent's runtime, so the check
// belongs to the form rather than to the worker. The failure it prevents is a
// task that queues, starts a Pod and only then discovers there is nothing to run.
func TestValidateRunner(t *testing.T) {
	cases := []struct {
		name        string
		goal        store.AgentGoal
		runtimeType string
		wantErr     string
		wantRunner  string
	}{
		{
			name: "empty runner defaults to the prose loop",
			goal: store.AgentGoal{}, runtimeType: runtimetype.OpenCode, wantRunner: store.RunnerProse,
		},
		{
			name: "an unknown runner is refused",
			goal: store.AgentGoal{Runner: "magic"}, runtimeType: runtimetype.Langflow, wantErr: "실행 방식",
		},
		{
			name:        "a runtime without a flow engine cannot run flows",
			goal:        store.AgentGoal{Runner: store.RunnerFlow, FlowID: "abc", StartOnDemand: true},
			runtimeType: runtimetype.OpenCode, wantErr: "이 실행 방식을 지원하지 않습니다",
		},
		{
			name:        "a flow runner needs a flow",
			goal:        store.AgentGoal{Runner: store.RunnerFlow, StartOnDemand: true},
			runtimeType: runtimetype.Langflow, wantErr: "흐름을 선택",
		},
		{
			name:        "a flow runs inside the runtime, so the runtime has to start",
			goal:        store.AgentGoal{Runner: store.RunnerFlow, FlowID: "abc"},
			runtimeType: runtimetype.Langflow, wantErr: "Runtime 시작",
		},
		{
			name:        "a runtime with no ACP agent cannot be driven by the protocol",
			goal:        store.AgentGoal{Runner: store.RunnerACP, StartOnDemand: true},
			runtimeType: runtimetype.Langflow, wantErr: "이 실행 방식을 지원하지 않습니다",
		},
		{
			name:        "an ACP goal on an agent that speaks it is accepted",
			goal:        store.AgentGoal{Runner: store.RunnerACP, StartOnDemand: true},
			runtimeType: runtimetype.QwenCode, wantRunner: store.RunnerACP,
		},
		{
			name: "an ACP goal that asks a person to approve cannot also approve everything itself",
			goal: store.AgentGoal{Runner: store.RunnerACP, StartOnDemand: true,
				CLIApprovalMode: "yolo", ApprovalRequired: true},
			runtimeType: runtimetype.QwenCode, wantErr: "yolo",
		},
		{
			name:        "a complete flow goal is accepted",
			goal:        store.AgentGoal{Runner: store.RunnerFlow, FlowID: "  abc  ", FlowOutputComponent: " ChatOutput-1 ", StartOnDemand: true},
			runtimeType: runtimetype.Langflow, wantRunner: store.RunnerFlow,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			goal := item.goal
			err := validateRunner(&goal, item.runtimeType)
			if item.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), item.wantErr) {
					t.Fatalf("error = %v, want one mentioning %q", err, item.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if goal.Runner != item.wantRunner {
				t.Errorf("runner = %q, want %q", goal.Runner, item.wantRunner)
			}
			if goal.Runner == store.RunnerFlow && (goal.FlowID != "abc" || goal.FlowOutputComponent != "ChatOutput-1") {
				t.Errorf("the flow identifiers were not trimmed: %q / %q", goal.FlowID, goal.FlowOutputComponent)
			}
		})
	}
}

// Switching back to the prose loop keeps the flow somebody already picked, so
// changing one's mind twice in the console does not lose the choice.
func TestValidateRunnerKeepsTheFlowWhenSwitchingBack(t *testing.T) {
	goal := store.AgentGoal{Runner: store.RunnerProse, FlowID: "kept", StartOnDemand: false}
	if err := validateRunner(&goal, runtimetype.Langflow); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if goal.FlowID != "kept" {
		t.Errorf("flow id = %q, want it kept", goal.FlowID)
	}
}
