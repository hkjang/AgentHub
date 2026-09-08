package policy

import (
	"fmt"
	"testing"
)

// "Nobody may send resident registration numbers to a model" is the sentence this
// package opens with, and the operator who writes its other half — nobody may send
// them to an outside tool either — writes one rule with a data class on it.
//
// That rule reached no Pod. Compiling asks a rule whether it applies to this agent
// and this server, with nothing scanned, and a rule naming data classes does not
// match an unscanned request: it was dropped, and the gateway was provisioned as
// though the rule had never been written. What decided instead was the class's own
// global action, so a deployment that had set 주민등록번호 to 기록만 and written
// the rule to catch the rest sent them and kept a finding.
//
// The gateway is where the scan happens. It is the only place that can answer such
// a rule, which is why the classes now travel on the compiled rule rather than
// being resolved away here.
func TestARuleAboutADataClassReachesThePod(t *testing.T) {
	document := Document{Rules: []Rule{
		{ID: "no-rrn-outward", Effect: Deny, Actions: []string{ActionToolCall},
			Servers: []string{"jira"}, DataClasses: []string{"rrn"}, Reason: "주민등록번호는 외부 도구로 보낼 수 없습니다"},
	}}
	compiled := CompileServer(document, Request{Agent: "상담 에이전트", AgentID: "a-1", Server: "jira"})

	if compiled.Empty() {
		t.Fatal("a rule that refuses calls is something to say about this server, so the binding has to carry it")
	}
	if len(compiled.Rules) != 1 {
		t.Fatalf("the rule did not survive compilation: %#v", compiled)
	}
	if len(compiled.Rules[0].DataClasses) != 1 || compiled.Rules[0].DataClasses[0] != "rrn" {
		t.Fatalf("the class travelled without its condition: %#v", compiled.Rules[0])
	}
	// The condition is the whole rule: an ordinary call to the same tool is not
	// what the operator wrote it about.
	if got := Decide(compiled, "jira", "create_issue"); got != Allow {
		t.Errorf("a call carrying nothing gives %q, want %q", got, Allow)
	}
	if got := DecideData(compiled, "jira", "create_issue", []string{"rrn"}); got != Deny {
		t.Errorf("a call carrying a 주민등록번호 gives %q, want %q", got, Deny)
	}
	if got := DecideData(compiled, "jira", "create_issue", []string{"email"}); got != Allow {
		t.Errorf("a call carrying another class gives %q, want %q", got, Allow)
	}
}

// The summary lists cannot say "this tool, but only when the call carries a
// 주민등록번호". Written into them the rule would refuse every call to the tool,
// which is not what it says — so it travels in the ordered rules alone, and a Pod
// that reads only the summary is left exactly as it is today.
func TestADataClassRuleIsNotSummarisedIntoARestriction(t *testing.T) {
	compiled := CompileServer(Document{Rules: []Rule{
		{ID: "no-rrn-outward", Effect: Deny, Actions: []string{ActionToolCall},
			DataClasses: []string{"rrn"}, Reason: "차단"},
		{ID: "gate-secrets", Effect: RequireApproval, Actions: []string{ActionToolCall},
			Tools: []string{"create_issue"}, DataClasses: []string{"secret"}, Reason: "승인"},
	}}, Request{Agent: "상담 에이전트", Server: "jira"})

	if compiled.DenyAll || compiled.GateAll {
		t.Fatalf("a conditional rule became an unconditional one: %#v", compiled)
	}
	if len(compiled.Denied) != 0 || len(compiled.Gated) != 0 {
		t.Fatalf("a conditional rule was summarised as a tool restriction: %#v", compiled)
	}
	if got := olderGatewayDecides(compiled, "jira", "create_issue"); got != Allow {
		t.Errorf("an older Pod gives %q for an ordinary call, want %q", got, Allow)
	}
}

// The property, over every arrangement of three rules and every set of classes a
// scan can report: what the gateway was given decides the call exactly as the
// document does. The bug above was not wrong logic — it was a selector nobody had
// put through this comparison.
func TestTheCompiledRulesAgreeWithTheDocumentOverEveryDataClass(t *testing.T) {
	pool := []Rule{
		{Effect: Allow, DataClasses: []string{"rrn"}},
		{Effect: Allow, Tools: []string{"read_*"}},
		{Effect: Deny, DataClasses: []string{"rrn"}, Reason: "주민등록번호 금지"},
		{Effect: Deny, Tools: []string{"delete_*"}, DataClasses: []string{"card", "rrn"}, Reason: "차단"},
		{Effect: Deny, Reason: "서버 차단"},
		{Effect: RequireApproval, DataClasses: []string{"card"}, Reason: "승인"},
		{Effect: RequireApproval, Tools: []string{"create_issue"}, Reason: "승인"},
	}
	tools := []string{"read_file", "create_issue", "delete_repo", "a_new_tool"}
	classSets := [][]string{nil, {"rrn"}, {"card"}, {"rrn", "card"}, {"email"}}
	request := Request{Agent: "상담 에이전트", AgentID: "a-1", Server: "jira"}

	for _, first := range pool {
		for _, second := range pool {
			for _, third := range pool {
				for _, fallback := range []string{"", Deny, RequireApproval} {
					document := Document{DefaultEffect: fallback, Rules: []Rule{first, second, third}}
					for index := range document.Rules {
						document.Rules[index].ID = fmt.Sprint(index + 1)
						document.Rules[index].Actions = []string{ActionToolCall}
						document.Rules[index].Servers = []string{"jira"}
					}
					if err := document.Validate(); err != nil {
						t.Fatalf("the sweep wrote a document the platform would reject: %v", err)
					}
					compiled := CompileServer(document, request)
					for _, tool := range tools {
						for _, classes := range classSets {
							want := Evaluate(document, Request{Action: ActionToolCall, Agent: request.Agent,
								AgentID: request.AgentID, Server: "jira", Tool: tool, DataClasses: classes}).Effect
							if got := DecideData(compiled, "jira", tool, classes); got != want {
								t.Fatalf("%v (default %q): the gateway gives %q for %s carrying %v while the document says %q (compiled %#v)",
									[]string{describe(first), describe(second), describe(third)},
									fallback, got, tool, classes, want, compiled)
							}
						}
					}
				}
			}
		}
	}
}

func describe(rule Rule) string {
	return fmt.Sprintf("%s%v%v", rule.Effect, rule.Tools, rule.DataClasses)
}
