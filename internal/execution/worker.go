package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/hkjang/AgentHub/internal/store"
)

// Worker drains the task queue.
//
// It is a separate process from the API so a long agent run can never block a
// request, and several workers can run at once: tasks are claimed with
// FOR UPDATE SKIP LOCKED and held on a lease, so a worker that dies releases its
// task instead of stranding it.
type Worker struct {
	store        *store.Store
	orchestrator *Orchestrator
	logger       *slog.Logger
	id           string

	// Concurrency is the floor: the worker always offers at least this many
	// slots. MaxConcurrency is the ceiling it may scale up to when the queue
	// backs up; equal values disable scaling.
	Concurrency    int
	MaxConcurrency int
	// ScaleInterval is how often the queue is measured.
	ScaleInterval time.Duration
	// PollInterval is how long to wait after finding an empty queue.
	PollInterval time.Duration
	// Lease is how long a claim is held before another worker may take over.
	Lease time.Duration
}

func NewWorker(db *store.Store, orchestrator *Orchestrator, logger *slog.Logger, id string) *Worker {
	return &Worker{
		store: db, orchestrator: orchestrator, logger: logger, id: id,
		Concurrency: 2, MaxConcurrency: 2, PollInterval: 3 * time.Second, Lease: 5 * time.Minute,
		ScaleInterval: 10 * time.Second,
	}
}

// NewWorkerID produces a stable-looking identifier for logs and task ownership.
func NewWorkerID(hostname string) string {
	if hostname == "" {
		hostname = "worker"
	}
	return fmt.Sprintf("%s-%s", hostname, uuid.NewString()[:8])
}

// Run drains the queue until the context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.MaxConcurrency < w.Concurrency {
		w.MaxConcurrency = w.Concurrency
	}
	// The channel is sized for the ceiling and the scaler holds the difference,
	// so the limit can move without rebuilding it under running tasks.
	slots := make(chan struct{}, w.MaxConcurrency)
	scale := newScaler(slots, w.Concurrency, w.MaxConcurrency)
	w.logger.Info("execution worker started", "worker", w.id,
		"concurrency", w.Concurrency, "maxConcurrency", w.MaxConcurrency)
	if w.MaxConcurrency > w.Concurrency {
		go w.autoscale(ctx, scale)
	}
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("execution worker stopping", "worker", w.id)
			return ctx.Err()
		case slots <- struct{}{}:
		}

		task, err := w.store.ClaimAgentTask(ctx, w.id, w.Lease)
		if err != nil {
			<-slots
			if errors.Is(err, store.ErrNotFound) {
				if !sleep(ctx, w.PollInterval) {
					return ctx.Err()
				}
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.logger.Error("task claim failed", "worker", w.id, "error", err)
			if !sleep(ctx, w.PollInterval) {
				return ctx.Err()
			}
			continue
		}

		go func(task store.AgentTask) {
			defer func() { <-slots }()
			// A panic in one task must not take the worker down with it.
			defer func() {
				if recovered := recover(); recovered != nil {
					w.logger.Error("task panicked", "task", task.ID, "panic", recovered)
					_ = w.store.FinishAgentTask(context.WithoutCancel(ctx), task.ID, store.TaskFailed, fmt.Sprintf("실행 중 예기치 못한 오류: %v", recovered))
				}
			}()
			w.execute(ctx, task)
		}(task)
	}
}

// execute runs one task and applies the retry policy to the outcome.
func (w *Worker) execute(ctx context.Context, task store.AgentTask) {
	traceID := "task-" + task.ID[:8] + "-" + fmt.Sprint(task.Attempts)
	logger := w.logger.With("worker", w.id, "task", task.ID, "agent", task.AgentID, "attempt", task.Attempts, "traceId", traceID)
	logger.Info("task claimed", "title", task.Title, "priority", task.Priority)

	// Keep the claim alive while the task runs, otherwise a long agent run would
	// be picked up a second time by another worker.
	heartbeat, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go w.keepClaim(heartbeat, task.ID)

	outcome, err := w.orchestrator.Execute(ctx, task, traceID)
	// Finalisation must survive shutdown, or a task would be left marked running.
	finish := context.WithoutCancel(ctx)
	if err != nil {
		logger.Error("task execution failed", "error", err)
	}

	// A task parked for approval has already been moved to waiting_approval by the
	// orchestrator; touching its status here would drag it back out of the gate.
	if errors.Is(outcome.parked, ErrAwaitingApproval) {
		logger.Info("task is waiting for approval")
		return
	}

	if outcome.Status == store.TaskCompleted {
		if err := w.store.FinishAgentTask(finish, task.ID, store.TaskCompleted, ""); err != nil {
			logger.Error("task completion not recorded", "error", err)
		}
		w.notify(finish, task, "작업을 완료했습니다", task.Title)
		w.publish(finish, task, store.EventTaskCompleted, map[string]any{"title": task.Title, "agentId": task.AgentID})
		logger.Info("task completed")
		return
	}

	goal, goalErr := w.store.AgentGoalByID(finish, task.AgentID)
	if goalErr != nil {
		goal = store.DefaultAgentGoal(task.AgentID)
	}
	// Only infrastructure failures are retried. An agent that did not meet its
	// goal will not meet it by being asked again unprompted.
	if outcome.Retryable && task.Attempts <= goal.MaxRetries {
		delay := backoff(task.Attempts)
		if err := w.store.RetryAgentTask(finish, task.ID, delay, outcome.Failure); err != nil {
			logger.Error("task retry not scheduled", "error", err)
		}
		logger.Warn("task will be retried", "delaySeconds", delay.Seconds(), "reason", outcome.Failure)
		return
	}

	status := store.TaskFailed
	if outcome.Retryable {
		// Out of attempts: park it for an operator instead of retrying forever.
		status = store.TaskDeadLetter
	}
	if err := w.store.FinishAgentTask(finish, task.ID, status, outcome.Failure); err != nil {
		logger.Error("task failure not recorded", "error", err)
	}
	w.notify(finish, task, "작업이 실패했습니다", task.Title+" — "+outcome.Failure)
	eventType := store.EventTaskFailed
	if status == store.TaskDeadLetter {
		eventType = store.EventTaskDeadLettered
	}
	w.publish(finish, task, eventType, map[string]any{"title": task.Title, "agentId": task.AgentID, "reason": outcome.Failure})
	logger.Warn("task finished unsuccessfully", "status", status, "reason", outcome.Failure)
}

// notify tells the owner their autonomous task finished. Nobody is watching the
// screen when a scheduled task runs, so the outcome has to come to them.
func (w *Worker) notify(ctx context.Context, task store.AgentTask, title, message string) {
	if err := w.store.CreateNotification(ctx, task.OwnerID, "task", title, message, "/tasks"); err != nil {
		w.logger.Warn("task notification not delivered", "task", task.ID, "error", err)
	}
}

// publish records what happened so event triggers can react to it. The task has
// already finished either way, so a publish failure is logged, not surfaced.
//
// The trigger that created this task is carried along as the cause, which is
// what stops an event trigger from waking itself in a loop.
func (w *Worker) publish(ctx context.Context, task store.AgentTask, eventType string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.logger.Warn("event payload could not be encoded", "task", task.ID, "type", eventType, "error", err)
		return
	}
	if err := w.store.PublishEvent(ctx, store.PlatformEvent{
		Type: eventType, OwnerID: task.OwnerID, SubjectType: "task", SubjectID: task.ID,
		Payload: body, CauseTriggerID: task.TriggerID,
	}); err != nil {
		w.logger.Warn("event could not be published", "task", task.ID, "type", eventType, "error", err)
	}
}

// keepClaim extends the lease periodically until the task finishes.
func (w *Worker) keepClaim(ctx context.Context, taskID string) {
	interval := w.Lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.ExtendTaskLease(ctx, taskID, w.id, w.Lease); err != nil {
				w.logger.Warn("task lease not extended", "task", taskID, "error", err)
			}
		}
	}
}

// backoff grows exponentially so a failing dependency is not hammered, and is
// capped so a task still retries within a useful window.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := math.Pow(2, float64(attempt)) * 5
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
