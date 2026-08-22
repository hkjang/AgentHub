package store

import (
	"os"
	"strings"
	"testing"
)

// A budget somebody can walk around by putting the agent in a graph is a
// suggestion. Workflow tokens have counted toward it since the tokens were
// recorded; the money was the honest half left out, because nothing knew which
// endpoint priced which step.
//
// The engine prices each step at the endpoint that answered it now, so the figure
// exists and the cost budget has to add it — both for a person and for a
// department.
func TestTheCostBudgetCountsWorkflowSpend(t *testing.T) {
	body, err := os.ReadFile("quota.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, scope := range []string{"func departmentExecutionUsage(", "func executionUsage("} {
		at := strings.Index(source, scope)
		if at < 0 {
			t.Fatalf("%s is gone; this guard is reading nothing", scope)
		}
		fn := source[at:]
		if end := strings.Index(fn, "\n}\n"); end >= 0 {
			fn = fn[:end]
		}
		if !strings.Contains(fn, "workflow_runs") {
			t.Errorf("%s does not count workflow spend at all", scope)
			continue
		}
		if !strings.Contains(fn, "sum(w.cost)") && !strings.Contains(fn, "sum(cost)") {
			t.Errorf("%s counts workflow tokens but not workflow money; the cost budget is walked around by a graph", scope)
		}
		if !strings.Contains(fn, "usage.Cost += workflowCost") {
			t.Errorf("%s reads the workflow cost and does not add it", scope)
		}
	}
}

// The cost is stored, not joined. A price corrected next month must not rewrite
// what a workflow cost this month, for the same reason a run records its rate.
func TestAWorkflowRunStoresWhatItCost(t *testing.T) {
	body, err := os.ReadFile("workflowrun.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) FinishWorkflowRun(")
	if at < 0 {
		t.Fatal("FinishWorkflowRun is gone; this guard is reading nothing")
	}
	finish := source[at:]
	if end := strings.Index(finish, "\n}\n"); end >= 0 {
		finish = finish[:end]
	}
	for _, part := range []string{"cost=$", "currency=$"} {
		if !strings.Contains(finish, part) {
			t.Errorf("a finished workflow run does not store %s; its money would have to be reconstructed later, from prices that move", part)
		}
	}
}
