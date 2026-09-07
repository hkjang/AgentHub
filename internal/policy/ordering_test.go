package policy

import (
	"fmt"
	"testing"
)

// The order the rules were written in is the policy. Compiling them into three
// lists kept every rule and dropped that, and the gateway then read the lists in
// a fixed order of its own: denial first.
//
// So a gate written above a deny — "a delete needs a person, and nothing else on
// this server is allowed at all" — arrived in the Pod as the deny alone. The
// document says the call waits for a reviewer; the console, the simulator and
// the audit trail all say so; the gateway refused it outright, and refusing is
// the one answer nobody follows up on.
func TestAGateWrittenAboveADenyStillWaitsForAPerson(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "gate-deletes", Effect: RequireApproval, Actions: []string{ActionToolCall},
			Servers: []string{"github"}, Tools: []string{"delete_*"}, Reason: "삭제는 승인 후"},
		{ID: "deny-server", Effect: Deny, Actions: []string{ActionToolCall},
			Servers: []string{"github"}, Reason: "서버 차단"},
	}}
	compiled := CompileServer(document, Request{Agent: "결산 에이전트", AgentID: "a-1", Server: "github"})

	for tool, want := range map[string]string{
		"delete_repo": RequireApproval,
		"delete_temp": RequireApproval,
		"read_file":   Deny,
		"a_new_tool":  Deny,
	} {
		if got := Decide(compiled, "github", tool); got != want {
			t.Errorf("the gateway gives %q for %s, want %q (compiled %#v)", got, tool, want, compiled)
		}
	}
}

// The default is a decision about every tool nobody wrote a rule for, and it is
// the one an operator sets first when they want a closed platform. It was never
// compiled at all: task.create and runtime.start honoured it because the API
// evaluates the document, and every tool call inside the Pod was allowed.
func TestTheDocumentDefaultReachesTheGateway(t *testing.T) {
	document := Document{DefaultEffect: Deny, Rules: []Rule{
		{ID: "allow-reads", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_*"}},
	}}
	compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})

	if compiled.Empty() {
		t.Fatal("a default of deny is something to say about every tool, so the binding has to carry it")
	}
	if got := Decide(compiled, "github", "read_file"); got != Allow {
		t.Errorf("the exception gives %q, want %q", got, Allow)
	}
	if got := Decide(compiled, "github", "delete_repo"); got != Deny {
		t.Errorf("a tool no rule named gives %q, want %q", got, Deny)
	}
}

// A default of allow is the shipped setting, and it must stay a no-op: a binding
// that gains a policy block on every provisioning is a policy nobody wrote.
func TestADefaultOfAllowSaysNothing(t *testing.T) {
	for _, document := range []Document{{}, {DefaultEffect: Allow}, {DefaultEffect: "무엇"}} {
		compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})
		if !compiled.Empty() || compiled.Default != "" {
			t.Errorf("%#v compiled to %#v, want nothing", document, compiled)
		}
	}
}

// The property behind both, over every arrangement of three rules this pool can
// write: what the gateway was given decides every tool exactly as the document
// does. Written as a sweep because the bugs above were not wrong logic — they
// were a shape nobody had put through the comparison.
func TestTheCompiledRulesAgreeWithTheDocumentOverEveryArrangement(t *testing.T) {
	pool := []Rule{
		{Effect: Allow, Tools: []string{"read_*"}},
		{Effect: Allow, Tools: []string{"delete_temp"}},
		{Effect: Allow},
		{Effect: Deny, Tools: []string{"delete_*"}, Reason: "쓰기 금지"},
		{Effect: Deny, Reason: "차단"},
		{Effect: RequireApproval, Tools: []string{"github/delete_repo", "run_shell"}, Reason: "승인"},
		{Effect: RequireApproval, Reason: "승인"},
	}
	tools := []string{"read_file", "list_issues", "delete_repo", "delete_temp", "run_shell", "a_new_tool"}
	request := Request{Agent: "결산 에이전트", AgentID: "a-1", Server: "github"}

	for _, first := range pool {
		for _, second := range pool {
			for _, third := range pool {
				for _, fallback := range []string{"", Deny, RequireApproval} {
					document := Document{DefaultEffect: fallback, Rules: []Rule{first, second, third}}
					for index := range document.Rules {
						document.Rules[index].ID = fmt.Sprint(index + 1)
						document.Rules[index].Actions = []string{ActionToolCall}
					}
					if err := document.Validate(); err != nil {
						t.Fatalf("the sweep wrote a document the platform would reject: %v", err)
					}
					compiled := CompileServer(document, request)
					for _, tool := range tools {
						want := Evaluate(document, Request{Action: ActionToolCall, Agent: request.Agent,
							AgentID: request.AgentID, Server: "github", Tool: tool}).Effect
						if got := Decide(compiled, "github", tool); got != want {
							t.Fatalf("%v (default %q): the gateway gives %q for %s while the document says %q (compiled %#v)",
								[]string{first.Effect + fmt.Sprint(first.Tools), second.Effect + fmt.Sprint(second.Tools), third.Effect + fmt.Sprint(third.Tools)},
								fallback, got, tool, want, compiled)
						}
					}
				}
			}
		}
	}
}

// A Pod provisioned before the ordered rules existed reads the summary lists and
// nothing else, so they are still compiled and still say what they always said.
func TestAnOlderPodStillReadsTheSummary(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "allow-read", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_file"}},
		{ID: "deny-server", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "차단"},
	}}
	compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})
	for tool, want := range map[string]string{"read_file": Allow, "delete_repo": Deny} {
		if got := olderGatewayDecides(compiled, "github", tool); got != want {
			t.Errorf("an older Pod gives %q for %s, want %q (compiled %#v)", got, tool, want, compiled)
		}
	}
}
