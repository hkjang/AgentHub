package execution

import "testing"

func newTestScaler(min, max int) (*scaler, chan struct{}) {
	slots := make(chan struct{}, max)
	return newScaler(slots, min, max), slots
}

// The worker starts at the floor the operator set, not at the ceiling: the
// ceiling is what it may grow to under load, not what it should idle at.
func TestScalerStartsAtTheFloor(t *testing.T) {
	s, slots := newTestScaler(2, 8)
	if s.limit() != 2 {
		t.Fatalf("limit = %d, want the floor", s.limit())
	}
	if len(slots) != 6 {
		t.Fatalf("scaler should hold %d tokens, holds %d", 6, len(slots))
	}
}

func TestScalerGrowsWithTheBacklog(t *testing.T) {
	s, _ := newTestScaler(2, 8)
	if moved := s.reconcile(5, 0); moved <= 0 {
		t.Fatalf("a backlog of 5 should raise the limit, moved %d", moved)
	}
	if s.limit() != 5 {
		t.Fatalf("limit = %d, want 5", s.limit())
	}
	// Running tasks count too: they are occupying the slots that the queued
	// ones are waiting for.
	s.reconcile(4, 4)
	if s.limit() != 8 {
		t.Fatalf("limit = %d, want the ceiling", s.limit())
	}
}

func TestScalerNeverExceedsTheCeilingOrDropsBelowTheFloor(t *testing.T) {
	s, _ := newTestScaler(2, 4)
	s.reconcile(100, 100)
	if s.limit() != 4 {
		t.Fatalf("limit = %d, want the ceiling even under a flood", s.limit())
	}
	for range scaleDownAfter * 3 {
		s.reconcile(0, 0)
	}
	if s.limit() != 2 {
		t.Fatalf("limit = %d, want the floor when idle", s.limit())
	}
}

// A queue that empties for one tick is usually a gap between tasks. Dropping
// the limit immediately and raising it again churns runtimes for nothing.
func TestScalerWaitsBeforeScalingDown(t *testing.T) {
	s, _ := newTestScaler(1, 6)
	s.reconcile(6, 0)
	if s.limit() != 6 {
		t.Fatalf("setup failed: limit = %d", s.limit())
	}
	for i := range scaleDownAfter - 1 {
		if moved := s.reconcile(0, 0); moved != 0 {
			t.Fatalf("quiet pass %d must not scale down yet, moved %d", i+1, moved)
		}
	}
	if moved := s.reconcile(0, 0); moved >= 0 {
		t.Fatalf("the last quiet pass should scale down, moved %d", moved)
	}
	if s.limit() != 1 {
		t.Fatalf("limit = %d, want the floor", s.limit())
	}
}

// Work arriving during a quiet spell has to reset the countdown, or a steady
// trickle would still be treated as an idle plane.
func TestBacklogResetsTheQuietCount(t *testing.T) {
	s, _ := newTestScaler(1, 6)
	s.reconcile(6, 0)
	s.reconcile(0, 0)
	s.reconcile(0, 0)
	s.reconcile(6, 0) // busy again
	if moved := s.reconcile(0, 0); moved != 0 {
		t.Fatalf("the countdown should have restarted, moved %d", moved)
	}
	if s.limit() != 6 {
		t.Fatalf("limit = %d, want it held at 6", s.limit())
	}
}

// Scaling down must never interrupt work: a token in use by a running task
// cannot be taken, so the reduction lands as tasks finish.
func TestScalingDownDoesNotStealTokensInUse(t *testing.T) {
	s, slots := newTestScaler(1, 4)
	s.reconcile(4, 0)
	if s.limit() != 4 {
		t.Fatalf("setup failed: limit = %d", s.limit())
	}
	// Three tasks are running: three tokens are out.
	for range 3 {
		slots <- struct{}{}
	}
	for range scaleDownAfter {
		s.reconcile(0, 0)
	}
	// Only the one free token could be reclaimed, so the effective limit is 3
	// until a task finishes — and no running task was disturbed.
	if len(slots) != 4 {
		t.Fatalf("channel should be full of in-use and held tokens, got %d", len(slots))
	}
	if s.limit() != 3 {
		t.Fatalf("limit = %d, want 3 while three tasks still hold tokens", s.limit())
	}
	// Each finished task lets the pending reduction take one more token, and it
	// retries on the next pass rather than waiting out the quiet period again.
	<-slots
	s.reconcile(0, 0)
	if s.limit() != 2 {
		t.Fatalf("limit = %d, want 2 with two tasks still running", s.limit())
	}
	<-slots
	<-slots
	s.reconcile(0, 0)
	if s.limit() != 1 {
		t.Fatalf("limit = %d, want the floor once every token came back", s.limit())
	}
}

// An operator who set no ceiling gets exactly the fixed behaviour they had.
func TestEqualBoundsMeanNoScaling(t *testing.T) {
	s, _ := newTestScaler(3, 3)
	if s.limit() != 3 {
		t.Fatalf("limit = %d, want 3", s.limit())
	}
	if moved := s.reconcile(50, 50); moved != 0 {
		t.Fatalf("a fixed worker must not scale up, moved %d", moved)
	}
	for range scaleDownAfter * 2 {
		if moved := s.reconcile(0, 0); moved != 0 {
			t.Fatalf("a fixed worker must not scale down, moved %d", moved)
		}
	}
}

func TestScalerRepairsImpossibleBounds(t *testing.T) {
	s, _ := newTestScaler(0, 0)
	if s.limit() != 1 {
		t.Fatalf("a floor below one must be repaired, limit = %d", s.limit())
	}
	slots := make(chan struct{}, 2)
	if got := newScaler(slots, 5, 2).limit(); got != 5 {
		t.Fatalf("a ceiling under the floor must follow the floor, limit = %d", got)
	}
}
