package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func warmGoal(mutate func(*store.AgentGoal)) store.AgentGoal {
	goal := store.DefaultAgentGoal("agent-1")
	goal.Description = "야간 점검"
	if mutate != nil {
		mutate(&goal)
	}
	return goal
}

// A warm-up window holds a Pod for its whole length, so an unbounded one is a
// runtime that is simply never off — the opposite of what the pool is for.
func TestWarmupWindowIsBounded(t *testing.T) {
	for _, seconds := range []int{-1, 1801, 86400} {
		goal := warmGoal(func(g *store.AgentGoal) { g.WarmupSeconds = seconds })
		if err := validateGoal(&goal); err == nil {
			t.Errorf("warmupSeconds=%d must be rejected", seconds)
		}
	}
	for _, seconds := range []int{0, 1, 300, 1800} {
		goal := warmGoal(func(g *store.AgentGoal) { g.WarmupSeconds = seconds })
		if err := validateGoal(&goal); err != nil {
			t.Errorf("warmupSeconds=%d should be accepted: %v", seconds, err)
		}
	}
}

func TestKeepWarmWindowIsBounded(t *testing.T) {
	for _, seconds := range []int{-1, 3601} {
		goal := warmGoal(func(g *store.AgentGoal) { g.KeepWarmSeconds = seconds; g.StopAfterTask = true })
		if err := validateGoal(&goal); err == nil {
			t.Errorf("keepWarmSeconds=%d must be rejected", seconds)
		}
	}
	goal := warmGoal(func(g *store.AgentGoal) { g.KeepWarmSeconds = 600; g.StopAfterTask = true })
	if err := validateGoal(&goal); err != nil {
		t.Errorf("a bounded keep-warm window should be accepted: %v", err)
	}
}

// Holding a runtime after a task only means anything when the task would
// otherwise have stopped it; accepting it silently would leave an operator
// believing they had configured something.
func TestKeepWarmRequiresStopAfterTask(t *testing.T) {
	goal := warmGoal(func(g *store.AgentGoal) { g.KeepWarmSeconds = 300; g.StopAfterTask = false })
	err := validateGoal(&goal)
	if err == nil {
		t.Fatal("keep-warm without stop-after-task must be rejected")
	}
	if !strings.Contains(err.Error(), "중지") {
		t.Fatalf("the error should say which setting is missing: %v", err)
	}
	goal.StopAfterTask = true
	if err := validateGoal(&goal); err != nil {
		t.Fatalf("with stop-after-task it is valid: %v", err)
	}
}

// The pool is opt-in: an agent that never asked for warming must validate
// exactly as it did before the feature existed.
func TestPoolIsOffByDefault(t *testing.T) {
	goal := store.DefaultAgentGoal("agent-1")
	if goal.WarmupSeconds != 0 || goal.KeepWarmSeconds != 0 {
		t.Fatalf("default goal must not enable the pool: %+v", goal)
	}
	if err := validateGoal(&goal); err != nil {
		t.Fatalf("the default goal must stay valid: %v", err)
	}
}
