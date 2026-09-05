package policy

import (
	"strings"
	"testing"
)

// gatewayDecides is what the in-Pod gateway does with a compiled rule set. It is
// the other half of every comparison below, because "the compiled form says the
// same thing as the document" is the only property that matters here — the
// gateway is the only place a tool call is actually refused.
func gatewayDecides(compiled ServerRules, server, tool string) string {
	return Decide(compiled, server, tool)
}

// olderGatewayDecides is the same question asked of a Pod that only reads the
// summary lists, which is every Pod provisioned before the ordered rules
// existed. The summary is still compiled for them, so this has to keep working.
func olderGatewayDecides(compiled ServerRules, server, tool string) string {
	return decideProjection(compiled, server, tool)
}

// The shape the effect exists for, and the one the documentation advertises: a
// narrow exception sitting above a broad deny.
//
// Both ends agreed about everything except the Pod. The simulator answered
// allow, the audit trail recorded the allow rule, task.create and runtime.start
// honoured it — and the gateway, the one place a tool call is refused, was
// provisioned with the deny alone, because a narrow allow compiled to nothing.
func TestANarrowAllowSurvivesCompilationAsAnException(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "allow-read", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_file", "list_*"}},
		{ID: "deny-server", Effect: Deny, Actions: []string{ActionToolCall}, Servers: []string{"github"}, Reason: "서버 차단"},
	}}
	request := Request{Agent: "결산 에이전트", AgentID: "a-1", Server: "github"}
	compiled := CompileServer(document, request)

	if !compiled.DenyAll {
		t.Fatalf("the broad deny was lost: %#v", compiled)
	}
	if strings.Join(compiled.Allowed, ",") != "list_*,read_file" {
		t.Fatalf("the exception did not reach the gateway: %#v", compiled)
	}
	for tool, want := range map[string]string{
		"read_file":    Allow,
		"list_issues":  Allow,
		"delete_repo":  Deny,
		"a_new_tool":   Deny,
		"read_file_v2": Deny,
	} {
		if got := gatewayDecides(compiled, "github", tool); got != want {
			t.Errorf("the gateway gives %q for %s, want %q", got, tool, want)
		}
	}
}

// The same allow written below the deny changes nothing: the deny matched first,
// so it decides at call time and it has to decide in the Pod too.
func TestAnExceptionBelowTheRestrictionDoesNotTravel(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "deny-writes", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"delete_*"}, Reason: "쓰기 금지"},
		{ID: "allow-temp", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"delete_temp"}},
	}}
	compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})
	if len(compiled.Allowed) > 0 {
		t.Fatalf("an exception the rule above already caught must not travel: %#v", compiled)
	}
	if got := gatewayDecides(compiled, "github", "delete_temp"); got != Deny {
		t.Fatalf("the gateway gives %q, want %q", got, Deny)
	}
}

// An exception the restriction above it cannot have caught is still an
// exception, so only the patterns that actually overlap are dropped.
func TestOnlyTheShadowedPatternsOfAnExceptionAreDropped(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "deny-writes", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"github/delete_*"}, Reason: "쓰기 금지"},
		{ID: "allow-some", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"delete_temp", "read_file"}},
		{ID: "deny-server", Effect: Deny, Actions: []string{ActionToolCall}, Servers: []string{"github"}, Reason: "서버 차단"},
	}}
	compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})
	if strings.Join(compiled.Allowed, ",") != "read_file" {
		t.Fatalf("delete_temp is covered by the deny above it and read_file is not: %#v", compiled)
	}
	// "github/delete_*" and "delete_temp" are written differently and name the
	// same call, which is why the overlap is decided on patterns rather than on
	// the strings.
	for tool, want := range map[string]string{"delete_temp": Deny, "read_file": Allow, "a_new_tool": Deny} {
		if got := gatewayDecides(compiled, "github", tool); got != want {
			t.Errorf("the gateway gives %q for %s, want %q", got, tool, want)
		}
	}
}

// And the property behind all of the above, over the arrangements an operator
// actually writes: what the gateway was given decides every tool the same way
// the document does. A divergence here is a rule that is enforced in the console
// and absent in the Pod, which no screen can show.
func TestTheCompiledRulesAgreeWithTheDocument(t *testing.T) {
	tools := []string{"read_file", "list_issues", "delete_repo", "delete_temp", "run_shell", "a_new_tool"}
	documents := map[string]Document{
		"exception above a server-wide deny": {Rules: []Rule{
			{ID: "1", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_file"}},
			{ID: "2", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "차단"},
		}},
		"exception above a server-wide gate": {Rules: []Rule{
			{ID: "1", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_*"}},
			{ID: "2", Effect: RequireApproval, Actions: []string{ActionToolCall}, Reason: "승인"},
		}},
		"exception between two restrictions": {Rules: []Rule{
			{ID: "1", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"run_shell"}, Reason: "셸 금지"},
			{ID: "2", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"delete_temp"}},
			{ID: "3", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"delete_*"}, Reason: "쓰기 금지"},
		}},
		"exception written below the restriction": {Rules: []Rule{
			{ID: "1", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"delete_*"}, Reason: "쓰기 금지"},
			{ID: "2", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"delete_temp"}},
		}},
		"server-qualified exception": {Rules: []Rule{
			{ID: "1", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"github/read_file"}},
			{ID: "2", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "차단"},
		}},
		"a gate above a server-wide deny": {Rules: []Rule{
			{ID: "1", Effect: RequireApproval, Actions: []string{ActionToolCall}, Tools: []string{"delete_*"}, Reason: "승인 후 삭제"},
			{ID: "2", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "차단"},
		}},
		"a deny above a server-wide gate": {Rules: []Rule{
			{ID: "1", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"run_shell"}, Reason: "셸 금지"},
			{ID: "2", Effect: RequireApproval, Actions: []string{ActionToolCall}, Reason: "승인"},
		}},
		"a server-wide gate above a server-wide deny": {Rules: []Rule{
			{ID: "1", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"run_shell"}, Reason: "셸 금지"},
			{ID: "2", Effect: RequireApproval, Actions: []string{ActionToolCall}, Reason: "승인"},
			{ID: "3", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "차단"},
		}},
		"a default of deny": {DefaultEffect: Deny, Rules: []Rule{
			{ID: "1", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_*", "list_*"}},
		}},
		"a default of approval with nothing else to say": {DefaultEffect: RequireApproval},
		"a blanket allow ends it": {Rules: []Rule{
			{ID: "1", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"run_shell"}, Reason: "셸 금지"},
			{ID: "2", Effect: Allow, Actions: []string{ActionToolCall}, Tools: []string{"read_file"}},
			{ID: "3", Effect: Allow, Actions: []string{ActionToolCall}},
			{ID: "4", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "닿지 않음"},
		}},
	}
	for name, document := range documents {
		compiled := CompileServer(document, Request{Agent: "결산 에이전트", Server: "github"})
		for _, tool := range tools {
			want := Evaluate(document, Request{Action: ActionToolCall, Agent: "결산 에이전트", Server: "github", Tool: tool}).Effect
			if got := gatewayDecides(compiled, "github", tool); got != want {
				t.Errorf("%s: the gateway gives %q for %s while the document says %q (compiled %#v)", name, got, tool, want, compiled)
			}
		}
	}
}
