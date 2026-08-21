package execution

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// The record one real investigation produced, trimmed to the fields the platform
// reads. It is a real shape rather than an invented one: the field names, the
// nesting of a tool result and the fact that `data` arrives as a JSON string
// were all read off a run of the actual agent.
const investigationRecord = `{
  "result": "# 근본 원인\n\ncheckout 파드가 메모리 한도에 걸려 재시작했습니다.",
  "num_llm_calls": 3,
  "total_tokens": 760,
  "prompt_tokens": 600,
  "completion_tokens": 160,
  "tool_calls": [
    {
      "tool_call_id": "call_1",
      "tool_name": "fetch_webpage",
      "description": "Internet: Fetch Webpage http://prometheus/api",
      "toolset_name": "internet",
      "result": {"status": "success", "error": null, "data": "{\"result\": 42}"}
    },
    {
      "tool_call_id": "call_2",
      "tool_name": "kubectl_describe",
      "description": "Kubernetes: describe pod checkout",
      "toolset_name": "kubernetes/core",
      "result": {"status": "error", "error": "kubectl not found", "data": null}
    }
  ]
}`

// What the platform keeps from an investigation: the conclusion it will be
// judged on, the evidence behind it, and what the run actually cost.
func TestAnInvestigationIsReadWithItsEvidence(t *testing.T) {
	report, err := parseInvestigation(investigationRecord, "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(report.Conclusion, "메모리 한도") {
		t.Errorf("conclusion = %q", report.Conclusion)
	}
	if len(report.Evidence) != 2 {
		t.Fatalf("evidence = %#v", report.Evidence)
	}
	// A JSON string result is unwrapped, so the evidence reads as what the tool
	// returned rather than as an escaped blob.
	if report.Evidence[0].Data != `{"result": 42}` || report.Evidence[0].Toolset != "internet" {
		t.Errorf("first piece of evidence = %#v", report.Evidence[0])
	}
	// A tool that failed is evidence too — an investigation that concluded
	// something while half its queries failed is worth knowing about.
	if report.Evidence[1].Status != "error" || !strings.Contains(report.Evidence[1].Data, "kubectl not found") {
		t.Errorf("failed evidence = %#v", report.Evidence[1])
	}
	if report.failedEvidence() != 1 {
		t.Errorf("failed count = %d", report.failedEvidence())
	}
	if got := report.toolsets(); len(got) != 2 || got[0] != "internet" {
		t.Errorf("toolsets = %v", got)
	}
	// Real usage, reported by the agent, so an investigation is metered like any
	// other work rather than described as free.
	if report.TotalTokens != 760 || report.PromptTokens != 600 || report.CompletionTokens != 160 {
		t.Errorf("usage = %d/%d/%d", report.TotalTokens, report.PromptTokens, report.CompletionTokens)
	}
	if report.LLMCalls != 3 {
		t.Errorf("llm calls = %d", report.LLMCalls)
	}
}

// A run that produced no record, or produced something that is not one, has to
// say why in a sentence somebody can act on — the reason is on stderr, because
// stdout is where the record would have been.
func TestAFailedInvestigationExplainsItself(t *testing.T) {
	cases := []struct {
		name     string
		stdout   string
		stderr   string
		exitCode int
		want     string
	}{
		{
			name: "nothing at all", stderr: "litellm.APIConnectionError: connection refused", exitCode: 1,
			want: "connection refused",
		},
		{
			name: "not the record", stdout: "Investigating...\nSomething went wrong", exitCode: 2,
			stderr: "toolset prometheus failed to load", want: "toolset prometheus failed to load",
		},
		{
			name: "no reason given", exitCode: 137, want: "종료 코드 137",
		},
		{
			// The shape a real failure arrives in: this agent renders its traceback
			// as a drawn box, so the last line is a border and the one above it is a
			// frame of source code. Reading the last line literally reported
			// "…(종료 코드 1): ╰────╯" while the reason sat a few lines up.
			name: "a failure drawn in a box", exitCode: 1,
			stderr: "╭──────── Traceback (most recent call last) ─────────╮\n" +
				"│ /opt/agenthub/venv/lib/python3.12/holmes/config.py │\n" +
				"│    54 │                                            │\n" +
				"│    55 │   @staticmethod                            │\n" +
				"╰────────────────────────────────────────────────────╯\n" +
				"ValueError: Invalid data type: <class 'NoneType'>, expected dict or list.\n",
			want: "ValueError: Invalid data type",
		},
		{
			// Nothing but decoration still has to produce something rather than a
			// sentence that trails off after a colon.
			name: "nothing but a box", exitCode: 1,
			stderr: "╭────────────╮\n╰────────────╯\n", want: "종료 코드 1",
		},
		{
			// An empty conclusion is a failure even at exit zero: the run happened,
			// spent tokens and answered nothing, and reporting it as success would
			// hand the evaluator an empty transcript to judge.
			name: "a record with no conclusion", stdout: `{"result": "  ", "total_tokens": 40}`,
			want: "결론이 비어 있습니다",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			_, err := parseInvestigation(item.stdout, item.stderr, item.exitCode)
			if err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("error = %v, want one mentioning %q", err, item.want)
			}
		})
	}
}

// Reading metrics is what an investigation is. Running shell commands to find out
// more is a different kind of act, and it stays behind the modes somebody chose
// deliberately — the same modes every other in-Pod backend uses.
func TestShellStaysBehindADeliberateChoice(t *testing.T) {
	for mode, want := range map[string]string{
		"": "deny", "plan": "deny", "default": "deny", "auto-edit": "deny",
		"auto": "allow", "yolo": "allow",
	} {
		goal := store.AgentGoal{ApprovalMode: mode}
		if got := investigateShellPolicy(goal); got != want {
			t.Errorf("mode %q = %q, want %q", mode, got, want)
		}
		command := investigateCommand(runtimetype.Holmes, goal, resolvedModel{ModelName: "gpt-5"}, "why?")
		flag := "--bash-always-deny"
		if want == "allow" {
			flag = "--bash-always-allow"
		}
		if !contains(command, flag) {
			t.Errorf("mode %q built %v, want it to carry %s", mode, command, flag)
		}
	}
}

// The question is a positional argument, so a question that begins with a dash
// must not be read as a flag. Everything the Goal sets has to arrive too.
func TestTheQuestionCannotBecomeAFlag(t *testing.T) {
	goal := store.AgentGoal{MaxSteps: 12, ApprovalMode: "default"}
	command := investigateCommand(runtimetype.Holmes, goal, resolvedModel{ModelName: "gpt-5"}, "--verbose is not a flag here")

	separator := -1
	for index, argument := range command {
		if argument == "--" {
			separator = index
		}
	}
	if separator < 0 || separator != len(command)-2 {
		t.Fatalf("the question is not last after a separator: %v", command)
	}
	if command[len(command)-1] != "--verbose is not a flag here" {
		t.Errorf("question = %q", command[len(command)-1])
	}
	if !contains(command, "--max-steps") || !contains(command, "12") {
		t.Errorf("the step limit did not reach the agent: %v", command)
	}
	// The provider prefix is how the agent's model client is told which protocol
	// to speak; the endpoint comes from the generated configuration.
	if !contains(command, "openai/gpt-5") {
		t.Errorf("the model did not reach the agent: %v", command)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
