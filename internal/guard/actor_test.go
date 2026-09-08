package guard

import (
	"testing"

	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// The sentence the policy package opens with is "contractors may not call
// anything that writes, and nobody may send resident registration numbers to a
// model". The second half is a rule about a person and a data class, and this
// boundary is the only place it is ever decided.
//
// It was decided without the person. The request carried the action, the agent
// and the classes the scanner found, and left Role, User and UserID empty — so
// a rule naming a role matched nothing here, the global action for the class
// decided instead, and a deployment that had written 계약직 → 차단 and left the
// class on 기록만 posted the number to the model and recorded the finding.
//
// Nothing said so. The console's simulator is given a user, so it went on
// answering 차단 for the very request that was being allowed.
func TestARuleAboutAPersonDecidesAtTheModelBoundary(t *testing.T) {
	document := policy.Document{Rules: []policy.Rule{{
		ID: "no-rrn-from-contractors", Effect: policy.Deny,
		Actions: []string{policy.ActionModelCall}, Roles: []string{"contractor"},
		DataClasses: []string{"rrn"}, Reason: "계약직은 주민등록번호를 모델로 보낼 수 없습니다.",
	}}}
	step := workflow.Step{ID: "task", AgentID: "agent-1", AgentName: "지원 봇", OwnerID: "user-1"}
	actor := store.User{ID: "user-1", Username: "kim", Role: "contractor"}

	decision := policy.Evaluate(document, requestFor(policy.ActionModelCall, step, actor, []string{"rrn"}))
	if decision.Effect != policy.Deny {
		t.Fatalf("the contractor's prompt was decided %q by %q; the rule names their role", decision.Effect, decision.RuleID)
	}

	// The same request as the console's simulator is given, which is what an
	// operator checked the rule against before saving it. The two must agree:
	// that agreement is the only reason the simulator is worth reading.
	simulated := policy.Evaluate(document, policy.Request{
		Action: policy.ActionModelCall, Role: actor.Role, User: actor.Username, UserID: actor.ID,
		Agent: step.AgentName, AgentID: step.AgentID, DataClasses: []string{"rrn"},
	})
	if simulated.Effect != decision.Effect || simulated.RuleID != decision.RuleID {
		t.Errorf("the simulator answers %q/%q and the boundary %q/%q for the same request",
			simulated.Effect, simulated.RuleID, decision.Effect, decision.RuleID)
	}
}

// A rule can name the person rather than their role, and the flow boundary —
// every backend the worker runs passes through it — is held to the same rule.
func TestARuleNamingTheUserDecidesAtTheFlowBoundary(t *testing.T) {
	document := policy.Document{Rules: []policy.Rule{{
		ID: "hold-park", Effect: policy.Deny,
		Actions: []string{policy.ActionWorkflowRun}, Users: []string{"park"},
		Reason: "이 계정의 자동 실행은 보류 중입니다.",
	}}}
	step := workflow.Step{ID: "acp", AgentID: "agent-2", AgentName: "배포 봇", OwnerID: "user-2"}

	byName := policy.Evaluate(document, requestFor(policy.ActionWorkflowRun, step,
		store.User{ID: "user-2", Username: "park", Role: "user"}, []string{"card"}))
	if byName.Effect != policy.Deny {
		t.Errorf("a rule naming the user decided %q at the flow boundary", byName.Effect)
	}
	// The rule names one person; everybody else's run is unaffected by it.
	byOther := policy.Evaluate(document, requestFor(policy.ActionWorkflowRun, step,
		store.User{ID: "user-3", Username: "lee", Role: "user"}, []string{"card"}))
	if byOther.Effect != policy.Allow {
		t.Errorf("a rule naming park decided %q for lee", byOther.Effect)
	}
}

// A step that names a boundary instead of an agent escapes every rule about
// that agent.
//
// This is what the planner and the completion judge did: they went to the model
// as "Planner" and "Completion Evaluator", with no agent id, carrying the
// task's own input and the run's whole transcript. A rule written about the
// agent — the narrowest and most common thing an operator writes — matched
// nothing there, and the global action for the class decided instead.
func TestARuleAboutAnAgentNeedsTheStepToSayWhichAgent(t *testing.T) {
	document := policy.Document{Rules: []policy.Rule{{
		ID: "support-bot-no-rrn", Effect: policy.Deny,
		Actions: []string{policy.ActionModelCall}, Agents: []string{"지원 봇"},
		DataClasses: []string{"rrn"}, Reason: "이 에이전트는 주민등록번호를 모델로 보낼 수 없습니다.",
	}}}
	owner := store.User{ID: "user-1", Username: "kim", Role: "user"}

	labelled := workflow.Step{ID: "plan", AgentName: "Planner", OwnerID: "user-1"}
	if decision := policy.Evaluate(document, requestFor(policy.ActionModelCall, labelled, owner, []string{"rrn"})); decision.Effect != policy.Allow {
		t.Fatalf("this test no longer describes the gap it guards: %q", decision.Effect)
	}

	// The same call, made as the agent it is being made for.
	named := workflow.Step{ID: "plan", AgentID: "agent-1", AgentName: "지원 봇", OwnerID: "user-1"}
	decision := policy.Evaluate(document, requestFor(policy.ActionModelCall, named, owner, []string{"rrn"}))
	if decision.Effect != policy.Deny || decision.RuleID != "support-bot-no-rrn" {
		t.Errorf("the agent's own planner prompt was decided %q by %q", decision.Effect, decision.RuleID)
	}
	// A rule may name the agent by id rather than by the name somebody typed,
	// and both have to reach this boundary.
	byID := policy.Document{Rules: []policy.Rule{{
		ID: "by-id", Effect: policy.Deny, Actions: []string{policy.ActionModelCall},
		Agents: []string{"agent-1"}, Reason: "x",
	}}}
	if decision := policy.Evaluate(byID, requestFor(policy.ActionModelCall, named, owner, []string{"rrn"})); decision.Effect != policy.Deny {
		t.Errorf("a rule naming the agent's id decided %q", decision.Effect)
	}
}

// An owner nobody can read leaves the request as it always was rather than
// refusing the run: the runtime gate and the task gate both start the work when
// the owner is unreadable, and one boundary that stops instead would take the
// platform down over a query.
func TestAnUnreadableOwnerIsNotADecision(t *testing.T) {
	document := policy.Document{Rules: []policy.Rule{{
		ID: "contractors", Effect: policy.Deny, Actions: []string{policy.ActionModelCall},
		Roles: []string{"contractor"}, Reason: "x",
	}}}
	step := workflow.Step{ID: "task", AgentID: "agent-1", AgentName: "지원 봇"}
	if decision := policy.Evaluate(document, requestFor(policy.ActionModelCall, step, store.User{}, nil)); decision.Effect != policy.Allow {
		t.Errorf("an unknown owner was decided %q by a rule about a role", decision.Effect)
	}
}
