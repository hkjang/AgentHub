// Package quota bounds what the execution plane may spend.
//
// The platform already limited what a user could hold — runtimes, CPU, memory,
// storage — but nothing limited what they could run. One person could keep every
// worker slot busy, and an agent stuck in a loop could spend a month's token
// budget overnight with nobody watching, because the only spend signal was a
// report somebody had to open.
//
// The rules live here rather than in the worker so that the enqueue path and the
// run path answer the same question the same way, and so the answer is testable
// without a database.
package quota

import "fmt"

// Policy is the part of the governance settings this package enforces. Zero
// means unlimited for every field: a deployment that never configures quotas
// behaves exactly as it did before they existed.
type Policy struct {
	// MaxRunningTasksPerUser bounds how many of one user's tasks may be executing
	// at once, across every agent they own.
	MaxRunningTasksPerUser int `json:"maxRunningTasksPerUser"`
	// TokenBudgetPerUser is how many tokens one user's agents may spend in the
	// reporting window before their tasks stop being run.
	TokenBudgetPerUser int64 `json:"tokenBudgetPerUser"`
	// CostBudgetPerUser is the same limit in money, for deployments that priced
	// their model endpoints. Tokens spent on an endpoint with no price are not
	// counted here — they are counted in tokens, which is why both exist.
	CostBudgetPerUser float64 `json:"costBudgetPerUser"`
}

// Usage is what has been spent, measured over the same window the console's
// usage report shows.
type Usage struct {
	// RunningTasks is how many of this user's tasks are executing right now,
	// excluding the one being decided.
	RunningTasks int
	// Tokens and Cost are this user's spend in the window.
	Tokens int64
	Cost   float64
	// Currency labels Cost, for the message the user reads.
	Currency string
	// AgentTokens is the spend of the one agent about to run, for its own budget.
	AgentTokens int64
}

// Decision is what to do with a task.
type Decision struct {
	// Allowed lets the task run.
	Allowed bool
	// Wait means the limit is temporary — someone else's task will finish — so the
	// task goes back on the queue rather than failing. A retry budget is not spent
	// on waiting.
	Wait bool
	// Reason is written to the task and shown to its owner. It says which limit
	// and by how much, because "quota exceeded" is not something anyone can act
	// on.
	Reason string
}

var allow = Decision{Allowed: true}

// Scoped is one level's limits and the spend measured against them.
//
// The fields are named for what they bound rather than for whose settings they
// came from, because the same three limits now arrive from two places: the
// person's own resolved quota, and the capacity their department holds together.
// A department that has spent its month stops its members even when each of them
// is inside their own budget — and the refusal has to say which, because one of
// those is fixed by waiting and the other by asking for more.
type Scoped struct {
	// Scope is ScopeUser or ScopeDepartment; it names the limit in the refusal.
	Scope       string
	MaxRunning  int
	TokenBudget int64
	CostBudget  float64
	Usage       Usage
}

// UserScope is the person's own level, built from the platform policy shape the
// governance settings use.
func UserScope(policy Policy, usage Usage) Scoped {
	return Scoped{Scope: ScopeUser, MaxRunning: policy.MaxRunningTasksPerUser,
		TokenBudget: policy.TokenBudgetPerUser, CostBudget: policy.CostBudgetPerUser, Usage: usage}
}

// DepartmentScope is the department's total, built from the limits an
// administrator set on the department itself.
func DepartmentScope(limits Limits, usage Usage) Scoped {
	return Scoped{Scope: ScopeDepartment, MaxRunning: limits.MaxRunningTasks,
		TokenBudget: limits.TokenBudget, CostBudget: limits.CostBudget, Usage: usage}
}

// Empty reports a scope that bounds nothing, so a caller can skip measuring what
// nobody limited.
func (s Scoped) Empty() bool {
	return s.MaxRunning == 0 && s.TokenBudget == 0 && s.CostBudget == 0
}

// Evaluate applies the agent's own budget and every scope that bounds the task.
//
// Concurrency is checked first, across every scope: it is the limit that clears
// on its own, and a task that must wait should not be told about a budget it may
// never reach. A budget that is already spent fails the task instead of queueing
// it, because a window that resets in days is not something to hold a worker slot
// for.
//
// The agent's own budget sits between them. It is the narrowest limit and the one
// whose owner can act immediately, so a runaway agent is named as the cause
// rather than the person or the team it belongs to.
func Evaluate(agentBudget int64, scopes ...Scoped) Decision {
	for _, scope := range scopes {
		if scope.MaxRunning > 0 && scope.Usage.RunningTasks >= scope.MaxRunning {
			return Decision{Wait: true, Reason: fmt.Sprintf(
				"%s 동시 실행 한도(%d개)에 도달해 대기 중입니다. 앞선 작업이 끝나면 이어서 실행됩니다.",
				scopeName(scope.Scope), scope.MaxRunning)}
		}
	}
	// The agent's own spend is the same number whichever scope reports it, so it
	// is read from the first one that measured anything.
	if len(scopes) > 0 && agentBudget > 0 && scopes[0].Usage.AgentTokens >= agentBudget {
		return Decision{Reason: fmt.Sprintf(
			"이 Agent의 토큰 예산(%s)을 모두 사용했습니다. 최근 사용량 %s 토큰.",
			number(agentBudget), number(scopes[0].Usage.AgentTokens))}
	}
	for _, scope := range scopes {
		if scope.TokenBudget > 0 && scope.Usage.Tokens >= scope.TokenBudget {
			return Decision{Reason: fmt.Sprintf(
				"%s 토큰 예산(%s)을 모두 사용했습니다. 최근 사용량 %s 토큰.",
				scopeName(scope.Scope), number(scope.TokenBudget), number(scope.Usage.Tokens))}
		}
		if scope.CostBudget > 0 && scope.Usage.Cost >= scope.CostBudget {
			currency := scope.Usage.Currency
			if currency == "" {
				currency = "KRW"
			}
			return Decision{Reason: fmt.Sprintf(
				"%s 비용 예산(%.2f %s)을 모두 사용했습니다. 최근 사용액 %.2f %s.",
				scopeName(scope.Scope), scope.CostBudget, currency, scope.Usage.Cost, currency)}
		}
	}
	return allow
}

// number formats a token count with thousands separators, because a budget
// message with a bare nine-digit number is unreadable.
func number(value int64) string {
	text := fmt.Sprintf("%d", value)
	if len(text) <= 3 {
		return text
	}
	var out []byte
	for index, digit := range []byte(text) {
		if index > 0 && (len(text)-index)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digit)
	}
	return string(out)
}
