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
		case reflect.Slice:
			field.Set(reflect.ValueOf([]string{"some_tool"}))
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

// And the field the sweep was written for, end to end from the document.
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
