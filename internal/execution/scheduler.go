package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Scheduler turns due cron triggers into queued tasks.
//
// It runs inside the worker so a deployment with no worker simply does not fire
// schedules, rather than queueing work nothing will ever pick up. Several
// workers may run it at once: MarkTriggerFired only succeeds for the worker that
// advances next_fire_at first, so a schedule fires exactly once per due time.
type Scheduler struct {
	store    *store.Store
	logger   *slog.Logger
	Interval time.Duration
	// Batch bounds how many triggers one tick may fire, so a backlog cannot
	// flood the queue in a single pass.
	Batch int
}

func NewScheduler(db *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: db, logger: logger, Interval: 20 * time.Second, Batch: 20}
}

func (s *Scheduler) Run(ctx context.Context) error {
	s.logger.Info("trigger scheduler started", "intervalSeconds", s.Interval.Seconds())
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	triggers, err := s.store.DueCronTriggers(ctx, s.Batch)
	if err != nil {
		s.logger.Error("due triggers could not be read", "error", err)
		return
	}
	for _, trigger := range triggers {
		s.fire(ctx, trigger)
	}
}

func (s *Scheduler) fire(ctx context.Context, trigger store.AgentTrigger) {
	next, err := NextFireAt(trigger.Schedule, trigger.Timezone, time.Now())
	if err != nil {
		// A schedule that cannot be parsed would otherwise be retried every tick
		// forever; disarm it and say why.
		s.logger.Error("trigger schedule is invalid, disabling its next fire", "trigger", trigger.ID, "schedule", trigger.Schedule, "error", err)
		if setErr := s.store.SetTriggerNextFire(ctx, trigger.ID, nil); setErr != nil {
			s.logger.Error("trigger could not be disarmed", "trigger", trigger.ID, "error", setErr)
		}
		return
	}

	// Advance the schedule first. Whoever wins this update owns this firing, so
	// two workers cannot both enqueue the same occurrence.
	won, err := s.store.MarkTriggerFired(ctx, trigger.ID, &next)
	if err != nil {
		s.logger.Error("trigger could not be advanced", "trigger", trigger.ID, "error", err)
		return
	}
	if !won {
		return
	}

	title := trigger.TaskTitle
	if title == "" {
		title = trigger.Name
	}
	task, err := s.store.CreateAgentTask(ctx, store.CreateTaskInput{
		AgentID: trigger.AgentID, OwnerID: trigger.OwnerID,
		Title: title, Input: trigger.TaskInput, Priority: trigger.Priority,
		Source: "cron", TriggerID: &trigger.ID, CreatedBy: trigger.OwnerID,
	})
	if err != nil {
		s.logger.Error("scheduled task could not be created", "trigger", trigger.ID, "error", err)
		return
	}
	s.logger.Info("scheduled task queued", "trigger", trigger.ID, "task", task.ID, "agent", trigger.AgentID, "nextFireAt", next)
}
