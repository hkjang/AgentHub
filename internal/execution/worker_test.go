package execution

import (
	"testing"

	"github.com/hkjang/AgentHub/internal/policy"
)

// The policy engine was consulted when a person queued a task from the console
// and nowhere else. A schedule, a webhook, an event or another agent could run
// an agent that policy forbids, and a nightly job created by somebody whose
// permission was later withdrawn went on firing unread.
//
// The decision itself is the policy package's; what matters here is that both
// refusing effects refuse. Treating require_approval as an allow would be the
// worst possible reading of a rule that asked for review.
func TestBothRefusingEffectsStopATask(t *testing.T) {
	document := policy.Document{Rules: []policy.Rule{
		{ID: "no-prod", Actions: []string{policy.ActionTaskCreate}, Agents: []string{"프로덕션 배포"},
			Effect: policy.Deny, Reason: "운영 배포 에이전트는 예약 실행할 수 없습니다."},
		{ID: "review-first", Actions: []string{policy.ActionTaskCreate}, Agents: []string{"비용 큰 분석"},
			Effect: policy.RequireApproval},
	}}
	for _, tc := range []struct {
		agent   string
		allowed bool
	}{
		{"프로덕션 배포", false},
		{"비용 큰 분석", false},
		{"평범한 에이전트", true},
	} {
		decision := policy.Evaluate(document, policy.Request{
			Action: policy.ActionTaskCreate, Role: "user", User: "somebody", Agent: tc.agent,
		})
		if decision.Allowed() != tc.allowed {
			t.Errorf("%s → allowed=%v, want %v (effect %q)", tc.agent, decision.Allowed(), tc.allowed, decision.Effect)
		}
	}
	// A deployment with no rules must behave exactly as it did before this check
	// existed, whatever the task is.
	empty := policy.Evaluate(policy.Document{}, policy.Request{Action: policy.ActionTaskCreate, Agent: "무엇이든"})
	if !empty.Allowed() {
		t.Errorf("an empty policy refused a task: %#v", empty)
	}
}
