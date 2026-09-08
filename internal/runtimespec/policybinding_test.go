package runtimespec

import (
	"reflect"
	"testing"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/runtime"
)

// The compiled policy is the only form the in-Pod gateway ever sees. A field that
// is computed here and dropped on the way is a restriction that exists in the
// document, in the console simulator and in the audit trail, and nowhere in the
// Pod — which is the one place it had to be, because the gateway is what the
// agent cannot route around.
//
// This is how PolicyGateAll was lost: "every tool on this server needs a person"
// compiled correctly and was assigned to nothing.
func TestEveryCompiledPolicyFieldReachesTheBinding(t *testing.T) {
	rules := policy.ServerRules{}
	value := reflect.ValueOf(&rules).Elem()
	for index := 0; index < value.NumField(); index++ {
		switch field := value.Field(index); field.Kind() {
		case reflect.Bool:
			field.SetBool(true)
		case reflect.String:
			field.SetString(policy.Deny)
		case reflect.Slice:
			// One element of whatever the slice holds: the sweep asks whether the
			// field travelled, not what was in it.
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		default:
			t.Fatalf("ServerRules.%s is a %s, which this sweep does not know how to fill",
				value.Type().Field(index).Name, field.Kind())
		}
	}

	binding := runtime.MCPBinding{}
	applyServerRules(&binding, rules)

	carried := reflect.ValueOf(binding)
	for index := 0; index < value.NumField(); index++ {
		name := "Policy" + value.Type().Field(index).Name
		field := carried.FieldByName(name)
		if !field.IsValid() {
			t.Fatalf("ServerRules.%s has no MCPBinding.%s to travel in",
				value.Type().Field(index).Name, name)
		}
		if field.IsZero() {
			t.Fatalf("MCPBinding.%s stayed empty: the gateway will never enforce it", name)
		}
	}
}

// The same question one level down. A compiled rule carries the two selectors the
// control plane cannot resolve — the tool, and what the scanner has to find in the
// call — and only the gateway can answer either. A field dropped between the two
// structs is a condition the Pod enforces without, which turns "refuse the calls
// carrying a 주민등록번호" into "refuse the calls".
func TestEveryCompiledRuleFieldReachesTheBinding(t *testing.T) {
	rule := policy.CompiledRule{}
	value := reflect.ValueOf(&rule).Elem()
	for index := 0; index < value.NumField(); index++ {
		switch field := value.Field(index); field.Kind() {
		case reflect.String:
			field.SetString(policy.Deny)
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		default:
			t.Fatalf("CompiledRule.%s is a %s, which this sweep does not know how to fill",
				value.Type().Field(index).Name, field.Kind())
		}
	}

	binding := runtime.MCPBinding{}
	applyServerRules(&binding, policy.ServerRules{Rules: []policy.CompiledRule{rule}})
	if len(binding.PolicyRules) != 1 {
		t.Fatalf("the rule did not travel at all: %#v", binding)
	}

	carried := reflect.ValueOf(binding.PolicyRules[0])
	for index := 0; index < value.NumField(); index++ {
		name := value.Type().Field(index).Name
		field := carried.FieldByName(name)
		if !field.IsValid() {
			t.Fatalf("CompiledRule.%s has no runtime.PolicyRule.%s to travel in", name, name)
		}
		if field.IsZero() {
			t.Fatalf("runtime.PolicyRule.%s stayed empty: the gateway will enforce the rule without it", name)
		}
	}
}

// And the field that sweep was written for, end to end from the document: a rule
// about a data class was compiled away here entirely, because compiling asks it
// about a request nothing has scanned.
func TestARuleAboutADataClassReachesTheBinding(t *testing.T) {
	rules := policy.CompileServer(policy.Document{Rules: []policy.Rule{
		{ID: "no-rrn-outward", Effect: policy.Deny, Actions: []string{policy.ActionToolCall},
			Servers: []string{"jira"}, DataClasses: []string{"rrn"}, Reason: "주민등록번호 금지"},
	}}, policy.Request{Agent: "상담 에이전트", Server: "jira"})
	binding := runtime.MCPBinding{Name: "jira"}
	applyServerRules(&binding, rules)

	if len(binding.PolicyRules) != 1 || len(binding.PolicyRules[0].DataClasses) != 1 {
		t.Fatalf("the rule did not reach the binding with its condition: %#v", binding.PolicyRules)
	}
	if binding.PolicyDenyAll || len(binding.PolicyDenied) != 0 {
		t.Fatalf("a conditional rule became an unconditional restriction: %#v", binding)
	}
}

// And the field the first sweep was written for, end to end from the document.
func TestAServerWideGateReachesTheBinding(t *testing.T) {
	rules := policy.CompileServer(policy.Document{Rules: []policy.Rule{
		{ID: "gate-server", Effect: policy.RequireApproval, Actions: []string{policy.ActionToolCall},
			Servers: []string{"github"}, Reason: "모든 도구 승인"},
	}}, policy.Request{Agent: "결산 에이전트", Server: "github"})
	binding := runtime.MCPBinding{Name: "github"}
	applyServerRules(&binding, rules)
	if !binding.PolicyGateAll {
		t.Fatalf("the gate did not reach the binding: %#v", binding)
	}
}

// The rules travel in the order they were written, because that order is what
// decides. Summarised into lists, "a delete needs a person, and nothing else is
// allowed" reached the Pod as the deny alone.
func TestTheRulesReachTheBindingInTheDocumentsOrder(t *testing.T) {
	rules := policy.CompileServer(policy.Document{DefaultEffect: policy.Deny, Rules: []policy.Rule{
		{ID: "gate-deletes", Effect: policy.RequireApproval, Actions: []string{policy.ActionToolCall},
			Servers: []string{"github"}, Tools: []string{"delete_*"}, Reason: "삭제는 승인 후"},
		{ID: "deny-server", Effect: policy.Deny, Actions: []string{policy.ActionToolCall},
			Servers: []string{"github"}, Reason: "서버 차단"},
	}}, policy.Request{Agent: "결산 에이전트", Server: "github"})
	binding := runtime.MCPBinding{Name: "github"}
	applyServerRules(&binding, rules)

	if len(binding.PolicyRules) != 2 {
		t.Fatalf("both rules have to travel: %#v", binding.PolicyRules)
	}
	if binding.PolicyRules[0].Effect != policy.RequireApproval || binding.PolicyRules[0].Tools[0] != "delete_*" {
		t.Errorf("the gate is no longer first: %#v", binding.PolicyRules)
	}
	if binding.PolicyDefault != policy.Deny {
		t.Errorf("the document default did not travel: %q", binding.PolicyDefault)
	}
}
