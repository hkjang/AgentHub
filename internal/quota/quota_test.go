package quota

import (
	"strings"
	"testing"
)

func TestNoPolicyMeansNoLimit(t *testing.T) {
	// A deployment that never configured quotas must behave exactly as it did
	// before they existed, whatever it has spent.
	decision := Evaluate(0, UserScope(Policy{}, Usage{RunningTasks: 99, Tokens: 10_000_000, Cost: 9999}))
	if !decision.Allowed {
		t.Fatalf("an unconfigured policy blocked a task: %#v", decision)
	}
}

func TestConcurrencyMakesATaskWaitRatherThanFail(t *testing.T) {
	policy := Policy{MaxRunningTasksPerUser: 2}
	if decision := Evaluate(0, UserScope(policy, Usage{RunningTasks: 1})); !decision.Allowed {
		t.Fatalf("a task under the limit was blocked: %#v", decision)
	}
	decision := Evaluate(0, UserScope(policy, Usage{RunningTasks: 2}))
	if decision.Allowed || !decision.Wait {
		t.Fatalf("a task at the limit must wait, not fail: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "동시 실행") {
		t.Fatalf("the reason does not say which limit: %q", decision.Reason)
	}
}

func TestASpentBudgetFailsTheTask(t *testing.T) {
	// Waiting for a window that rolls over in days would hold a worker slot for
	// nothing, so this is a refusal rather than a wait.
	decision := Evaluate(0, UserScope(Policy{TokenBudgetPerUser: 1000}, Usage{Tokens: 1000}))
	if decision.Allowed || decision.Wait {
		t.Fatalf("a spent budget should fail the task: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "1,000") {
		t.Fatalf("the reason should carry the numbers: %q", decision.Reason)
	}
}

func TestAnAgentBudgetBindsBeforeTheUsers(t *testing.T) {
	// The agent's own budget exists to stop one runaway agent without stopping
	// everything else its owner runs.
	decision := Evaluate(500, UserScope(Policy{TokenBudgetPerUser: 1_000_000}, Usage{Tokens: 600, AgentTokens: 500}))
	if decision.Allowed {
		t.Fatal("the agent budget was ignored")
	}
	if !strings.Contains(decision.Reason, "Agent") {
		t.Fatalf("the reason should name the agent budget: %q", decision.Reason)
	}
}

func TestConcurrencyIsDecidedBeforeBudget(t *testing.T) {
	// A task that has to wait should not be told about a budget it may never
	// reach — and a wait must not be turned into a failure by a budget check that
	// runs first.
	decision := Evaluate(0, UserScope(Policy{MaxRunningTasksPerUser: 1, TokenBudgetPerUser: 100}, Usage{RunningTasks: 5, Tokens: 1000}))
	if !decision.Wait {
		t.Fatalf("concurrency should be decided first: %#v", decision)
	}
}

func TestCostBudgetNamesItsCurrency(t *testing.T) {
	decision := Evaluate(0, UserScope(Policy{CostBudgetPerUser: 50}, Usage{Cost: 51.5, Currency: "USD"}))
	if decision.Allowed || !strings.Contains(decision.Reason, "USD") {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	// With no currency recorded the message still has to read as money.
	fallback := Evaluate(0, UserScope(Policy{CostBudgetPerUser: 50}, Usage{Cost: 51.5}))
	if !strings.Contains(fallback.Reason, "KRW") {
		t.Fatalf("unexpected decision: %#v", fallback)
	}
}

func TestUnpricedSpendStillCountsInTokens(t *testing.T) {
	// An endpoint with no price contributes nothing to cost, which is why the
	// token budget exists: the cost limit alone would never stop it.
	decision := Evaluate(0, UserScope(Policy{TokenBudgetPerUser: 100, CostBudgetPerUser: 1000}, Usage{Tokens: 150, Cost: 0}))
	if decision.Allowed {
		t.Fatal("unpriced spend escaped both budgets")
	}
}

func TestNumberIsReadable(t *testing.T) {
	for value, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := number(value); got != want {
			t.Fatalf("number(%d) = %q, want %q", value, got, want)
		}
	}
}

// A department has its own budget, and a member inside their own limit can still
// be stopped by it. The refusal has to say which, because one of those is fixed
// by the person and the other by the team.
func TestADepartmentBudgetStopsAMemberWhoIsInsideTheirOwn(t *testing.T) {
	user := UserScope(Policy{TokenBudgetPerUser: 1_000_000}, Usage{Tokens: 10})
	department := DepartmentScope(Limits{TokenBudget: 5000}, Usage{Tokens: 5000})
	decision := Evaluate(0, user, department)
	if decision.Allowed || decision.Wait {
		t.Fatalf("the department budget was ignored: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "부서") {
		t.Fatalf("the reason does not say whose limit it was: %q", decision.Reason)
	}
}

// The person's own limit is checked first, so somebody who is over their own
// budget is told about that rather than about the team's.
func TestThePersonsOwnLimitIsNamedFirst(t *testing.T) {
	user := UserScope(Policy{TokenBudgetPerUser: 100}, Usage{Tokens: 200})
	department := DepartmentScope(Limits{TokenBudget: 5000}, Usage{Tokens: 5000})
	decision := Evaluate(0, user, department)
	if !strings.Contains(decision.Reason, "사용자") {
		t.Fatalf("the reason should name the person's own budget: %q", decision.Reason)
	}
}

// A department's concurrency limit makes a task wait like any other concurrency
// limit — it clears when a colleague's task finishes — and waiting must win over
// a budget in another scope, so a waiting task does not spend a retry.
func TestADepartmentAtItsConcurrencyLimitWaits(t *testing.T) {
	user := UserScope(Policy{TokenBudgetPerUser: 10}, Usage{Tokens: 0})
	department := DepartmentScope(Limits{MaxRunningTasks: 3}, Usage{RunningTasks: 3})
	decision := Evaluate(0, user, department)
	if !decision.Wait {
		t.Fatalf("a full department should defer the task: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "부서") {
		t.Fatalf("the reason does not say whose limit it was: %q", decision.Reason)
	}
}

// A department that bounds nothing must not change any answer, so the caller can
// skip measuring it at all.
func TestAnEmptyDepartmentScopeChangesNothing(t *testing.T) {
	department := DepartmentScope(Limits{}, Usage{})
	if !department.Empty() {
		t.Fatal("a department with no limits should report itself empty")
	}
	if decision := Evaluate(0, UserScope(Policy{}, Usage{Tokens: 9_999_999}), department); !decision.Allowed {
		t.Fatalf("an empty department blocked a task: %#v", decision)
	}
}
