package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Operating the execution plane.
//
// The console could describe what the plane had done and could not do anything
// about what it was doing. A queue with no worker behind it looked exactly like a
// quiet queue; a task whose worker had died sat at 'running' where no claim query
// would ever look at it again; a dead-lettered event was a number with no way to
// try it again; and an upgrade meant either racing the scheduler or shutting the
// whole deployment down.

// WorkerStatus values.
const (
	WorkerRunning = "running"
	WorkerPaused  = "paused"
	WorkerStopped = "stopped"
)

// WorkerHeartbeatInterval is how often a worker refreshes its row, and
// WorkerStaleAfter is how long without one before it is presumed gone. The gap
// between them is deliberate: one missed heartbeat is a slow query, not a dead
// process.
const (
	WorkerHeartbeatInterval = 10 * time.Second
	WorkerStaleAfter        = 45 * time.Second
)

// ExecutionWorker is one worker process as it last reported itself.
type ExecutionWorker struct {
	ID             string    `json:"id"`
	Hostname       string    `json:"hostname"`
	Version        string    `json:"version"`
	Concurrency    int       `json:"concurrency"`
	MaxConcurrency int       `json:"maxConcurrency"`
	Running        int       `json:"running"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"startedAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
	// Stale is true when the heartbeat stopped. The row is kept rather than
	// deleted: a worker that vanished is the thing an operator needs to see.
	Stale bool `json:"stale"`
}

// RegisterWorker records a worker as it starts.
func (s *Store) RegisterWorker(ctx context.Context, worker ExecutionWorker) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO execution_workers
		(id,hostname,version,concurrency,max_concurrency,running,status,started_at,last_seen_at)
		VALUES($1,$2,$3,$4,$5,0,'running',now(),now())
		ON CONFLICT (id) DO UPDATE SET hostname=EXCLUDED.hostname, version=EXCLUDED.version,
			concurrency=EXCLUDED.concurrency, max_concurrency=EXCLUDED.max_concurrency,
			status='running', started_at=now(), last_seen_at=now()`,
		worker.ID, worker.Hostname, worker.Version, worker.Concurrency, worker.MaxConcurrency)
	return err
}

// WorkerHeartbeat refreshes one worker's row.
func (s *Store) WorkerHeartbeat(ctx context.Context, id string, running int, status string) error {
	if status == "" {
		status = WorkerRunning
	}
	_, err := s.pool.Exec(ctx, `UPDATE execution_workers
		SET running=$2, status=$3, last_seen_at=now() WHERE id=$1`, id, running, status)
	return err
}

// StopWorker marks a worker as having shut down on purpose, which is what
// separates a deployment from a crash in the list an operator reads.
func (s *Store) StopWorker(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE execution_workers SET status='stopped', running=0, last_seen_at=now() WHERE id=$1`, id)
	return err
}

// Workers lists what the execution plane is made of.
func (s *Store) Workers(ctx context.Context) ([]ExecutionWorker, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,hostname,version,concurrency,max_concurrency,running,status,started_at,last_seen_at,
		(status <> 'stopped' AND last_seen_at < now() - $1::interval) AS stale
		FROM execution_workers ORDER BY last_seen_at DESC`, WorkerStaleAfter.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ExecutionWorker{}
	for rows.Next() {
		var item ExecutionWorker
		if err := rows.Scan(&item.ID, &item.Hostname, &item.Version, &item.Concurrency, &item.MaxConcurrency,
			&item.Running, &item.Status, &item.StartedAt, &item.LastSeenAt, &item.Stale); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// LiveWorkers counts the workers currently able to take work, which is the
// figure that turns "nothing is happening" into "nothing can happen".
func (s *Store) LiveWorkers(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM execution_workers
		WHERE status='running' AND last_seen_at >= now() - $1::interval`, WorkerStaleAfter.String()).Scan(&count)
	return count, err
}

// ForgetStoppedWorkers removes rows for processes that shut down cleanly a while
// ago. A crashed worker is kept: it is evidence.
func (s *Store) ForgetStoppedWorkers(ctx context.Context, olderThan time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM execution_workers
		WHERE status='stopped' AND last_seen_at < now() - $1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ReclaimStuckTasks puts back tasks whose worker stopped reporting.
//
// A claim carries a lease, and until now nothing ever reaped one: the claim query
// only looks at queued and retrying rows, so a task left at 'running' by a worker
// that died was stranded where nothing would find it again. Reclaiming returns it
// to the queue with its attempt already counted — the attempt did happen, and
// pretending otherwise would let a task that reliably kills its worker loop
// forever.
//
// The run that was in flight is closed at the same time, otherwise the execution
// history would show a run that never ended.
func (s *Store) ReclaimStuckTasks(ctx context.Context, grace time.Duration) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const reason = "실행 중이던 워커가 응답하지 않아 작업을 회수했습니다."
	rows, err := tx.Query(ctx, `UPDATE agent_tasks
		SET status='queued', claimed_by='', claimed_until=NULL, last_error=$2, scheduled_at=now(), updated_at=now()
		WHERE status IN ('planning','ready','running','waiting_tool')
		  AND claimed_until IS NOT NULL AND claimed_until < now() - $1::interval
		RETURNING id`, grace.String(), reason)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs
		SET status='failed', failure_reason=$2, finished_at=now()
		WHERE task_id = ANY($1) AND status='running'`, ids, reason); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit(ctx)
}

// RequeueFilter selects the finished tasks an operator wants to run again.
type RequeueFilter struct {
	// Status is the terminal state to recover, dead_letter or failed. Nothing
	// else may be requeued in bulk: a completed task would run twice and a
	// cancelled one was stopped on purpose.
	Status string
	// AgentID and OwnerID narrow the recovery to one agent or one person, which
	// is how a single broken integration is retried without disturbing anything
	// else.
	AgentID string
	OwnerID string
	// Since bounds how far back to go, so recovering after an outage does not
	// also restart last month's failures.
	Since *time.Time
	// Limit caps one operation. A bulk requeue is a queue-filling action and
	// deserves a ceiling.
	Limit int
}

// normalise refuses what must not be requeued and bounds what may.
//
// Only the two terminal failures can be recovered: requeueing a completed task
// would run its work a second time, and a cancelled one was stopped on purpose.
// The ceiling is there because a bulk requeue fills the queue, and an operator
// who meant to recover today's failures should not be able to restart a year of
// them with one click.
func (filter RequeueFilter) normalise() (RequeueFilter, error) {
	if filter.Status != TaskDeadLetter && filter.Status != TaskFailed {
		return filter, errors.New("dead_letter 또는 failed 상태만 다시 실행할 수 있습니다")
	}
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 200
	}
	return filter, nil
}

// RequeueTasks puts finished tasks back on the queue and reports how many moved.
//
// Attempts are reset: an operator asking for this has fixed whatever caused the
// failure, and leaving the count would let a task that had exhausted its retries
// dead-letter again on its first attempt.
func (s *Store) RequeueTasks(ctx context.Context, filter RequeueFilter) (int, error) {
	filter, err := filter.normalise()
	if err != nil {
		return 0, err
	}
	since := time.Time{}
	if filter.Since != nil {
		since = *filter.Since
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status='queued', attempts=0, last_error='',
			claimed_by='', claimed_until=NULL, scheduled_at=now(), updated_at=now()
		WHERE id IN (
			SELECT id FROM agent_tasks
			WHERE status=$1
			  AND ($2 = '' OR agent_id=$2)
			  AND ($3 = '' OR owner_id=$3)
			  AND ($4::timestamptz IS NULL OR updated_at >= $4)
			ORDER BY updated_at DESC
			LIMIT $5)`,
		filter.Status, filter.AgentID, filter.OwnerID, nullTime(since), filter.Limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// nullTime turns a zero time into a SQL NULL, so "no bound" is expressed once.
func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

// RedeliverEvents puts dead-lettered events back in the outbox.
//
// An event that could not be delivered is kept rather than dropped precisely so
// that it can be tried again once whatever broke has been fixed. The delivery
// ledger still holds what each subscriber already received, so a redelivery does
// not create the tasks that did succeed a second time.
func (s *Store) RedeliverEvents(ctx context.Context, eventID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE platform_events
		SET dead_lettered_at=NULL, attempts=0, next_attempt_at=now(), last_error='', claimed_by='', claimed_until=NULL
		WHERE dead_lettered_at IS NOT NULL AND ($1 = '' OR id=$1)`, eventID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeadLetteredEvents lists what could not be delivered, so the operator deciding
// whether to redeliver can see what they would be sending.
func (s *Store) DeadLetteredEvents(ctx context.Context, limit int) ([]PlatformEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id,type,owner_id,subject_type,subject_id,payload,cause_trigger_id,created_at,dispatched_at,attempts,last_error
		FROM platform_events WHERE dead_lettered_at IS NOT NULL ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PlatformEvent{}
	for rows.Next() {
		var item PlatformEvent
		if err := rows.Scan(&item.ID, &item.Type, &item.OwnerID, &item.SubjectType, &item.SubjectID,
			&item.Payload, &item.CauseTriggerID, &item.CreatedAt, &item.DispatchedAt, &item.Attempts, &item.LastError); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RetentionPolicy is how long operational history is kept. Zero means keep.
type RetentionPolicy struct {
	// Runs covers finished runs and their steps, which is by far the largest
	// table on a busy deployment.
	RunDays int `json:"runDays"`
	// Events covers delivered events; undelivered and dead-lettered ones are
	// never swept, because they are still work.
	EventDays int `json:"eventDays"`
	// Tasks covers finished tasks. Their runs go with them regardless of RunDays,
	// since a run without its task is unreadable.
	TaskDays int `json:"taskDays"`
	// Audit is kept longest and swept last: it is the record a compliance review
	// asks for, and deleting it is the one cleanup that cannot be reconstructed.
	AuditDays int `json:"auditDays"`
}

// CleanupResult counts what a sweep removed, or would remove.
type CleanupResult struct {
	DryRun bool           `json:"dryRun"`
	Counts map[string]int `json:"counts"`
}

// Cleanup removes operational history older than the policy.
//
// dryRun counts without deleting, because an operator who has just typed "30
// days" into a box deserves to see the number before the rows are gone.
func (s *Store) Cleanup(ctx context.Context, policy RetentionPolicy, dryRun bool) (CleanupResult, error) {
	result := CleanupResult{DryRun: dryRun, Counts: map[string]int{}}
	// Order matters: tasks take their runs with them, so counting runs first
	// would double-count anything a task sweep is about to remove anyway.
	sweeps := []struct {
		name   string
		days   int
		count  string
		delete string
	}{
		{name: "tasks", days: policy.TaskDays,
			count:  `SELECT count(*) FROM agent_tasks WHERE status IN ('completed','failed','cancelled','dead_letter') AND updated_at < $1`,
			delete: `DELETE FROM agent_tasks WHERE status IN ('completed','failed','cancelled','dead_letter') AND updated_at < $1`},
		{name: "runs", days: policy.RunDays,
			count:  `SELECT count(*) FROM agent_runs WHERE finished_at IS NOT NULL AND finished_at < $1`,
			delete: `DELETE FROM agent_runs WHERE finished_at IS NOT NULL AND finished_at < $1`},
		{name: "events", days: policy.EventDays,
			count:  `SELECT count(*) FROM platform_events WHERE dispatched_at IS NOT NULL AND dispatched_at < $1`,
			delete: `DELETE FROM platform_events WHERE dispatched_at IS NOT NULL AND dispatched_at < $1`},
		{name: "audit", days: policy.AuditDays,
			count:  `SELECT count(*) FROM audit_events WHERE occurred_at < $1`,
			delete: `DELETE FROM audit_events WHERE occurred_at < $1`},
	}
	for _, sweep := range sweeps {
		if sweep.days <= 0 {
			continue
		}
		before := time.Now().UTC().AddDate(0, 0, -sweep.days)
		if dryRun {
			var count int
			if err := s.pool.QueryRow(ctx, sweep.count, before).Scan(&count); err != nil {
				return CleanupResult{}, fmt.Errorf("%s: %w", sweep.name, err)
			}
			result.Counts[sweep.name] = count
			continue
		}
		tag, err := s.pool.Exec(ctx, sweep.delete, before)
		if err != nil {
			return CleanupResult{}, fmt.Errorf("%s: %w", sweep.name, err)
		}
		result.Counts[sweep.name] = int(tag.RowsAffected())
	}
	return result, nil
}

// ValidateRetention refuses a policy that would delete history somebody is
// probably still reading.
func (policy RetentionPolicy) Validate() error {
	limits := []struct {
		name  string
		days  int
		floor int
	}{
		{"실행 기록", policy.RunDays, 7},
		{"이벤트", policy.EventDays, 3},
		{"작업", policy.TaskDays, 7},
		{"감사 로그", policy.AuditDays, 30},
	}
	for _, limit := range limits {
		if limit.days == 0 {
			continue
		}
		if limit.days < limit.floor {
			return fmt.Errorf("%s 보관 기간은 최소 %d일이어야 합니다", limit.name, limit.floor)
		}
		if limit.days > 3650 {
			return errors.New("보관 기간은 최대 3650일입니다")
		}
	}
	return nil
}

// OperationsSettingKey is the system_settings row the pause lives in.
const OperationsSettingKey = "operations"

// OperationsSettings is the execution plane's operational switch.
type OperationsSettings struct {
	// Paused stops workers claiming new tasks. Running tasks finish: stopping
	// mid-run would leave exactly the stranded rows this release fixes.
	Paused bool `json:"paused"`
	// Reason is shown to everyone whose work is waiting, because a queue that
	// stopped moving with no explanation is indistinguishable from a broken one.
	Reason   string     `json:"reason,omitempty"`
	PausedBy string     `json:"pausedBy,omitempty"`
	PausedAt *time.Time `json:"pausedAt,omitempty"`
	// Retention is swept by the worker once a day when set.
	Retention RetentionPolicy `json:"retention"`
}

func (settings OperationsSettings) Validate() error {
	if len(strings.TrimSpace(settings.Reason)) > 300 {
		return errors.New("중지 사유는 300자 이하여야 합니다")
	}
	return settings.Retention.Validate()
}
