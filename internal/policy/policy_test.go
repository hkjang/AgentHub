package policy

import (
	"strings"
	"testing"
)

func on() *bool  { value := true; return &value }
func off() *bool { value := false; return &value }

// Order is the policy. A reader has to be able to predict the answer by reading
// top to bottom, and the engine has to agree with them.
func TestEvaluateTakesTheFirstMatchingRule(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "allow-oncall", Effect: Allow, Actions: []string{ActionToolCall}, Users: []string{"oncall"}},
		{ID: "deny-writes", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"delete_*"}, Reason: "쓰기 도구는 금지"},
	}}
	// The narrow allow sits above the broad deny, which is the whole reason
	// explicit allows exist.
	if decision := Evaluate(document, Request{Action: ActionToolCall, User: "oncall", Tool: "delete_branch"}); !decision.Allowed() || decision.RuleID != "allow-oncall" {
		t.Fatalf("the exception must win: %#v", decision)
	}
	decision := Evaluate(document, Request{Action: ActionToolCall, User: "intern", Tool: "delete_branch"})
	if decision.Effect != Deny || decision.RuleID != "deny-writes" || decision.Reason == "" {
		t.Fatalf("the deny must apply with its reason: %#v", decision)
	}
	// Everything that would have matched is reported, so "why did my rule do
	// nothing" is answerable without bisecting the document.
	if len(decision.Matched) != 1 || decision.Matched[0] != "deny-writes" {
		t.Fatalf("matched rules are not reported: %#v", decision.Matched)
	}
}

func TestEvaluateDefaults(t *testing.T) {
	// An empty document changes nothing, which is what makes it safe to turn on.
	if decision := Evaluate(Document{}, Request{Action: ActionTaskCreate}); !decision.Allowed() {
		t.Fatalf("an empty policy must allow: %#v", decision)
	}
	if decision := Evaluate(Document{DefaultEffect: Deny}, Request{Action: ActionTaskCreate}); decision.Effect != Deny || decision.RuleID != "" {
		t.Fatalf("the default must decide when nothing matches: %#v", decision)
	}
	// A disabled rule is not a rule.
	document := Document{Rules: []Rule{{ID: "off", Effect: Deny, Enabled: off(), Reason: "x"}}}
	if decision := Evaluate(document, Request{Action: ActionToolCall}); !decision.Allowed() {
		t.Fatalf("a disabled rule must not decide: %#v", decision)
	}
	document.Rules[0].Enabled = on()
	if decision := Evaluate(document, Request{Action: ActionToolCall}); decision.Effect != Deny {
		t.Fatal("an enabled rule must decide")
	}
}

// Selectors are the part people get wrong, so each one is checked for both the
// match it should make and the one it should not.
func TestSelectors(t *testing.T) {
	cases := []struct {
		name    string
		rule    Rule
		request Request
		match   bool
	}{
		{name: "an empty selector matches everything",
			rule: Rule{Actions: []string{ActionToolCall}}, request: Request{Action: ActionToolCall, User: "anyone"}, match: true},
		{name: "an action selector excludes other actions",
			rule: Rule{Actions: []string{ActionToolCall}}, request: Request{Action: ActionTaskCreate}},
		{name: "a wildcard action matches everything",
			rule: Rule{Actions: []string{"*"}}, request: Request{Action: ActionRuntimeStart}, match: true},
		{name: "roles are exact",
			rule: Rule{Roles: []string{"manager"}}, request: Request{Action: ActionToolCall, Role: "user"}},
		{name: "a user matches by name",
			rule: Rule{Users: []string{"kim"}}, request: Request{Action: ActionToolCall, User: "kim"}, match: true},
		{name: "a user matches by id too, so a rule survives a rename",
			rule: Rule{Users: []string{"u-1"}}, request: Request{Action: ActionToolCall, User: "kim", UserID: "u-1"}, match: true},
		{name: "an agent matches by name",
			rule: Rule{Agents: []string{"결산 에이전트"}}, request: Request{Action: ActionToolCall, Agent: "결산 에이전트"}, match: true},
		{name: "a tool matches unqualified",
			rule: Rule{Tools: []string{"delete_branch"}}, request: Request{Action: ActionToolCall, Server: "github", Tool: "delete_branch"}, match: true},
		{name: "a tool matches qualified by server",
			rule: Rule{Tools: []string{"github/delete_*"}}, request: Request{Action: ActionToolCall, Server: "github", Tool: "delete_branch"}, match: true},
		{name: "the same tool on another server is not covered",
			rule: Rule{Tools: []string{"github/delete_*"}}, request: Request{Action: ActionToolCall, Server: "gitlab", Tool: "delete_branch"}},
		{name: "a tool rule never fires on a request with no tool",
			rule: Rule{Tools: []string{"*"}}, request: Request{Action: ActionTaskCreate}},
		{name: "matching is case-insensitive because names are typed by hand",
			rule: Rule{Tools: []string{"Delete_Branch"}}, request: Request{Action: ActionToolCall, Tool: "delete_branch"}, match: true},
		{name: "data classes only match a scanned request",
			rule: Rule{DataClasses: []string{"rrn"}}, request: Request{Action: ActionModelCall}},
		{name: "a data class matches when the scanner found it",
			rule: Rule{DataClasses: []string{"rrn"}}, request: Request{Action: ActionModelCall, DataClasses: []string{"email", "rrn"}}, match: true},
		{name: "every selector has to match, not just one",
			rule: Rule{Roles: []string{"user"}, Tools: []string{"delete_*"}}, request: Request{Action: ActionToolCall, Role: "user", Tool: "read_file"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.rule.ID, test.rule.Effect, test.rule.Reason = "r", Deny, "x"
			decision := Evaluate(Document{Rules: []Rule{test.rule}}, test.request)
			if matched := decision.Effect == Deny; matched != test.match {
				t.Fatalf("matched=%v, want %v (%#v)", matched, test.match, decision)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := Document{Rules: []Rule{{ID: "a", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "금지"}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a normal document must be accepted: %v", err)
	}
	cases := []struct {
		name     string
		document Document
		mentions string
	}{
		{name: "a rule needs an id", document: Document{Rules: []Rule{{Effect: Allow}}}, mentions: "ID"},
		{name: "ids are unique so an operator can point at one",
			document: Document{Rules: []Rule{{ID: "a", Effect: Allow}, {ID: "A", Effect: Allow}}}, mentions: "중복"},
		{name: "an unknown effect would silently never apply",
			document: Document{Rules: []Rule{{ID: "a", Effect: "maybe"}}}, mentions: "효과"},
		{name: "an unknown action would silently never match",
			document: Document{Rules: []Rule{{ID: "a", Effect: Allow, Actions: []string{"tool.invoke"}}}}, mentions: "동작"},
		{name: "an unknown role too",
			document: Document{Rules: []Rule{{ID: "a", Effect: Allow, Roles: []string{"root"}}}}, mentions: "역할"},
		// A refusal with no reason is a support ticket.
		{name: "a deny must say why", document: Document{Rules: []Rule{{ID: "a", Effect: Deny}}}, mentions: "사유"},
		{name: "an approval gate must say why too", document: Document{Rules: []Rule{{ID: "a", Effect: RequireApproval}}}, mentions: "사유"},
		{name: "an unknown default would decide everything",
			document: Document{DefaultEffect: "sometimes"}, mentions: "기본 정책"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.document.Validate()
			if err == nil {
				t.Fatal("the document must be refused")
			}
			if !strings.Contains(err.Error(), test.mentions) {
				t.Fatalf("the message must mention %q; got %q", test.mentions, err)
			}
		})
	}
}

// The gateway is a separate process with no database, so the restrictions it
// enforces are compiled into its configuration. What it receives has to mean the
// same thing the central document does.
func TestCompileServer(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "gate-writes", Effect: RequireApproval, Actions: []string{ActionToolCall}, Tools: []string{"github/delete_*"}, Reason: "쓰기 승인"},
		{ID: "deny-shell", Effect: Deny, Actions: []string{ActionToolCall}, Tools: []string{"run_shell"}, Reason: "셸 금지"},
		{ID: "other-agent", Effect: Deny, Actions: []string{ActionToolCall}, Agents: []string{"다른 에이전트"}, Reason: "무관"},
	}}
	request := Request{Agent: "결산 에이전트", AgentID: "a-1", Server: "github"}
	compiled := CompileServer(document, request)
	if strings.Join(compiled.Denied, ",") != "run_shell" || strings.Join(compiled.Gated, ",") != "github/delete_*" {
		t.Fatalf("compiled = %#v", compiled)
	}
	if compiled.DenyAll || compiled.GateAll {
		t.Fatalf("nothing here covers the whole server: %#v", compiled)
	}
	// Patterns, not resolved names: the tool list is not known until the server
	// runs, and a rule has to cover the tools nobody has seen yet.
	if !MatchTool(compiled.Gated, "github", "delete_branch") {
		t.Fatal("the gateway cannot match what it was given")
	}
	if MatchTool(compiled.Gated, "gitlab", "delete_branch") {
		t.Fatal("a server-qualified pattern reached another server")
	}

	// A rule with no tool selector covers the whole server.
	whole := CompileServer(Document{Rules: []Rule{
		{ID: "server-wide", Effect: Deny, Actions: []string{ActionToolCall}, Servers: []string{"github"}, Reason: "서버 차단"},
	}}, request)
	if !whole.DenyAll || len(whole.Denied) > 0 {
		t.Fatalf("a server-wide deny must not compile to a list: %#v", whole)
	}

	// The rule naming another agent applies to that agent and to no other.
	other := CompileServer(document, Request{Agent: "다른 에이전트", Server: "github"})
	if !other.DenyAll {
		t.Fatalf("the rule for the other agent did not apply to it: %#v", other)
	}
	if compiled.DenyAll {
		t.Fatal("a rule naming another agent reached this one")
	}

	// An exception above a restriction wins here exactly as it would at call time.
	exempt := CompileServer(Document{Rules: []Rule{
		{ID: "allow-oncall", Effect: Allow, Actions: []string{ActionToolCall}, Users: []string{"oncall"}},
		{ID: "deny-everything", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "기본 차단"},
	}}, Request{User: "oncall", Server: "github"})
	if !exempt.Empty() {
		t.Fatalf("the exception did not survive compilation: %#v", exempt)
	}
	restricted := CompileServer(Document{Rules: []Rule{
		{ID: "allow-oncall", Effect: Allow, Actions: []string{ActionToolCall}, Users: []string{"oncall"}},
		{ID: "deny-everything", Effect: Deny, Actions: []string{ActionToolCall}, Reason: "기본 차단"},
	}}, Request{User: "intern", Server: "github"})
	if !restricted.DenyAll {
		t.Fatalf("everyone else must still be denied: %#v", restricted)
	}

	// Nothing configured means nothing changes for a deployment that has not
	// written a policy.
	if !CompileServer(Document{}, request).Empty() {
		t.Fatal("an empty policy must compile to nothing")
	}
}
