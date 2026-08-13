package api

import "testing"

func TestWorkflowLevelsBuildsDeterministicDAG(t *testing.T) {
	steps := []workflowStep{
		{ID: "review", DependsOn: []string{"build", "data"}},
		{ID: "data"},
		{ID: "build"},
	}
	levels := workflowLevels(steps)
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %#v", levels)
	}
	if len(levels[0]) != 2 || levels[0][0] != "build" || levels[0][1] != "data" || levels[1][0] != "review" {
		t.Fatalf("unexpected deterministic ordering: %#v", levels)
	}
}

func TestWorkflowLevelsRejectsCycle(t *testing.T) {
	steps := []workflowStep{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}}
	if levels := workflowLevels(steps); levels != nil {
		t.Fatalf("cycle must not produce an execution plan: %#v", levels)
	}
}
