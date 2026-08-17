package execution

import (
	"context"
	"time"
)

// Auto scaling of a worker's concurrency.
//
// The worker ran a fixed number of tasks at once, set by an environment
// variable, which is wrong in both directions: sized for the burst it wastes a
// model gateway's connections and a cluster's memory all night, and sized for
// the quiet hours it drains a morning backlog one task at a time.
//
// The limit therefore follows the queue, between a floor and a ceiling the
// operator still sets. Scaling up is immediate because a backlog is already
// costing someone time; scaling down waits for several quiet passes, because a
// queue that empties for one tick is usually about to refill and dropping the
// limit only to raise it again churns runtimes for nothing.

// scaler adjusts how many task slots a worker offers.
//
// It never interrupts running work: reducing the limit means the scaler holds
// idle slot tokens, so the reduction takes effect as tasks finish.
type scaler struct {
	slots chan struct{}
	// held is the number of tokens the scaler is keeping out of circulation.
	held int
	min  int
	max  int
	// quiet counts consecutive passes with no backlog.
	quiet int
}

// scaleDownAfter is how many consecutive quiet passes precede a reduction.
const scaleDownAfter = 4

func newScaler(slots chan struct{}, min, max int) *scaler {
	if min < 1 {
		min = 1
	}
	if max < min {
		max = min
	}
	// The channel is sized for the ceiling, so the floor is reached by holding
	// the difference from the start.
	s := &scaler{slots: slots, min: min, max: max}
	s.hold(max - min)
	return s
}

// limit is the number of tasks the worker may run right now.
func (s *scaler) limit() int { return s.max - s.held }

// hold takes tokens out of circulation, lowering the limit. It never blocks: a
// token currently used by a running task is simply not available, and the
// reduction lands on the next one that finishes.
func (s *scaler) hold(count int) int {
	taken := 0
	for range count {
		select {
		case s.slots <- struct{}{}:
			s.held++
			taken++
		default:
			return taken
		}
	}
	return taken
}

// release puts tokens back, raising the limit.
func (s *scaler) release(count int) int {
	given := 0
	for range count {
		if s.held == 0 {
			return given
		}
		select {
		case <-s.slots:
			s.held--
			given++
		default:
			return given
		}
	}
	return given
}

// target is the limit the queue justifies.
//
// One slot per waiting task, since a task holds its slot for as long as an agent
// takes to think, and bounded by what the operator allowed.
func (s *scaler) target(depth, running int) int {
	desired := depth + running
	if desired < s.min {
		desired = s.min
	}
	if desired > s.max {
		desired = s.max
	}
	return desired
}

// reconcile moves the limit toward what the queue justifies and reports the
// change, or 0 when nothing moved.
func (s *scaler) reconcile(depth, running int) int {
	desired := s.target(depth, running)
	current := s.limit()
	switch {
	case desired > current:
		s.quiet = 0
		return s.release(desired - current)
	case desired < current:
		// Wait for the queue to stay quiet: one empty tick is usually a gap
		// between tasks, and churning the limit churns runtimes with it.
		s.quiet++
		if s.quiet < scaleDownAfter {
			return 0
		}
		// A token in use by a running task cannot be taken, so a reduction may
		// only land in part. Stay ready to retry on the next pass rather than
		// waiting out the full quiet period again for each remaining token.
		wanted := current - desired
		taken := s.hold(wanted)
		if taken < wanted {
			s.quiet = scaleDownAfter - 1
		} else {
			s.quiet = 0
		}
		return -taken
	default:
		s.quiet = 0
		return 0
	}
}

// autoscale reconciles the worker's concurrency against the queue until the
// context ends.
func (w *Worker) autoscale(ctx context.Context, s *scaler) {
	ticker := time.NewTicker(w.ScaleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		depth, running, err := w.store.TaskQueueDepth(ctx)
		if err != nil {
			w.logger.Warn("queue depth could not be read", "worker", w.id, "error", err)
			continue
		}
		if moved := s.reconcile(depth, running); moved != 0 {
			w.logger.Info("worker concurrency adjusted", "worker", w.id,
				"concurrency", s.limit(), "change", moved, "queued", depth, "running", running)
		}
	}
}
