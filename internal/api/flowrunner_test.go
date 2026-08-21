package api

import (
	"encoding/json"
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
			// No longer a conflict: under this backend the platform answers the
			// agent's permission requests itself, so a Goal that asks for human
			// approval gets it — anything that is not read-only goes to a person
			// whatever the mode says.
			name: "an ACP goal may ask a person to approve and still be permissive",
			goal: store.AgentGoal{Runner: store.RunnerACP, StartOnDemand: true,
				ApprovalMode: "yolo", ApprovalRequired: true},
			runtimeType: runtimetype.QwenCode, wantRunner: store.RunnerACP,
		},
		{
			// The headless runner still cannot: it hands the mode to the agent and
			// reads the result, so there would be nothing left to stop it.
			name: "a headless goal that asks a person to approve cannot also approve everything itself",
			goal: store.AgentGoal{Runner: store.RunnerCLI, StartOnDemand: true,
				ApprovalMode: "yolo", ApprovalRequired: true},
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

// The setting was renamed when it stopped belonging to one backend. A document
// written against the old name has to keep working: a GitOps file from last
// month says cliApprovalMode, and dropping its value silently would be a worse
// outcome than any name.
func TestTheOldApprovalModeNameIsStillAccepted(t *testing.T) {
	body := `{"executionMode":"task","runner":"acp","startOnDemand":true,"cliApprovalMode":"auto-edit"}`
	var input struct {
		LegacyApprovalMode string `json:"cliApprovalMode"`
		store.AgentGoal
	}
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = input.LegacyApprovalMode
	}
	if input.ApprovalMode != "auto-edit" {
		t.Errorf("approval mode = %q, want the value the old name carried", input.ApprovalMode)
	}
	// And the new name wins when both are present, so a client that sends both
	// gets what it asked for most recently rather than what it used to ask for.
	both := `{"approvalMode":"auto","cliApprovalMode":"plan"}`
	input.ApprovalMode, input.LegacyApprovalMode = "", ""
	if err := json.Unmarshal([]byte(both), &input); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = input.LegacyApprovalMode
	}
	if input.ApprovalMode != "auto" {
		t.Errorf("approval mode = %q, want the new name to win", input.ApprovalMode)
	}
}

// And a goal read back carries both names, so a client still reading the old one
// sees a value rather than an empty string.
func TestAGoalIsReadableUnderBothNames(t *testing.T) {
	goal := store.DefaultAgentGoal("agent-1")
	goal.ApprovalMode = "auto"
	raw, err := json.Marshal(store.WithLegacyNames(goal))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var seen map[string]any
	if err := json.Unmarshal(raw, &seen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if seen["approvalMode"] != "auto" || seen["cliApprovalMode"] != "auto" {
		t.Errorf("names = %v / %v", seen["approvalMode"], seen["cliApprovalMode"])
	}
}
