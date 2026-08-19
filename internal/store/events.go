package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event types the platform publishes. Keeping them as constants means a trigger
// can be validated against the list instead of silently never firing.
const (
	EventTaskCompleted    = "task.completed"
	EventTaskFailed       = "task.failed"
	EventTaskDeadLettered = "task.dead_lettered"
	// EventTaskHandoff fires when an agent hands a task to a person in the
	// runtime. It is subscribable because "somebody has to finish this by hand" is
	// exactly the kind of thing a team wants routed somewhere.
	EventTaskHandoff     = "task.handoff"
	EventApprovalDecided = "approval.decided"
	EventRuntimeFailed   = "runtime.failed"
	EventArtifactCreated = "artifact.created"
)

// PublishableEvents is the list an operator may subscribe a trigger to.
var PublishableEvents = []string{
	EventTaskCompleted, EventTaskFailed, EventTaskDeadLettered, EventTaskHandoff,
	EventApprovalDecided, EventRuntimeFailed, EventArtifactCreated,
}

func IsPublishableEvent(value string) bool {
	for _, candidate := range PublishableEvents {
		if candidate == value {
			return true
		}
	}
	return false
}

// PlatformEvent is one thing that happened, recorded for delivery.
type PlatformEvent struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	OwnerID        string          `json:"ownerId"`
	SubjectType    string          `json:"subjectType"`
	SubjectID      string          `json:"subjectId"`
	Payload        json.RawMessage `json:"payload"`
	CauseTriggerID *string         `json:"causeTriggerId,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	// DispatchedAt is set when delivery finished, not when it was attempted.
	DispatchedAt *time.Time `json:"dispatchedAt,omitempty"`
	// Attempts, LastError and DeadLetteredAt are the delivery record: an event
	// that could not be delivered says so instead of looking delivered.
	Attempts       int        `json:"attempts"`
	LastError      string     `json:"lastError,omitempty"`
	DeadLetteredAt *time.Time `json:"deadLetteredAt,omitempty"`
	// Deliveries is how many subscribers have a task for this event, and
	// DeliveredTo names them: the ledger answers "did it reach that agent?".
	Deliveries  int    `json:"deliveries"`
	DeliveredTo string `json:"deliveredTo,omitempty"`
}

// PublishEvent records an event for the dispatcher.
//
// Publishing must never fail the thing that produced the event: a task that
// finished has finished whether or not anything is listening. Callers therefore
// log the error and carry on.
func (s *Store) PublishEvent(ctx context.Context, event PlatformEvent) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO platform_events(id,type,owner_id,subject_type,subject_id,payload,cause_trigger_id)
		VALUES($1,$2,$3,$4,$5,$6,$7)`,
		event.ID, event.Type, event.OwnerID, event.SubjectType, event.SubjectID, event.Payload, event.CauseTriggerID)
	return err
}

// ClaimEvents takes a batch of events that are due for delivery and holds them on
// a lease, counting the attempt. It does not mark them delivered: that happens
// once delivery has actually finished, so a worker that dies mid-batch loses
// nothing — its lease expires and the events are claimed again.
//
// FOR UPDATE SKIP LOCKED plus the lease is what makes several workers safe.
func (s *Store) ClaimEvents(ctx context.Context, workerID string, lease time.Duration, limit int) ([]PlatformEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		WITH claimed AS (
			SELECT id FROM platform_events
			WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL
			  AND next_attempt_at <= now()
			  AND (claimed_until IS NULL OR claimed_until < now())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		)
		UPDATE platform_events e
		SET attempts = e.attempts + 1, claimed_by = $1, claimed_until = now() + $2::interval
		FROM claimed WHERE e.id = claimed.id
		RETURNING e.id, e.type, e.owner_id, e.subject_type, e.subject_id, e.payload, e.cause_trigger_id, e.created_at, e.attempts`,
		workerID, lease.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PlatformEvent{}
	for rows.Next() {
		var event PlatformEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.OwnerID, &event.SubjectType, &event.SubjectID,
			&event.Payload, &event.CauseTriggerID, &event.CreatedAt, &event.Attempts); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// MarkEventDelivered closes an event out: every subscriber that was going to get
// a task has one.
func (s *Store) MarkEventDelivered(ctx context.Context, eventID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE platform_events
		SET dispatched_at = now(), claimed_by = '', claimed_until = NULL, last_error = ''
		WHERE id = $1`, eventID)
	return err
}

// RescheduleEvent puts an event back for another attempt after a backoff, keeping
// what went wrong so the reason is visible rather than only in a log line.
func (s *Store) RescheduleEvent(ctx context.Context, eventID string, delay time.Duration, lastError string) error {
	_, err := s.pool.Exec(ctx, `UPDATE platform_events
		SET next_attempt_at = now() + $2::interval, claimed_by = '', claimed_until = NULL, last_error = $3
		WHERE id = $1`, eventID, delay.String(), lastError)
	return err
}

// DeadLetterEvent stops retrying an event nobody could deliver. The row stays:
// an undeliverable event is exactly what an operator needs to see.
func (s *Store) DeadLetterEvent(ctx context.Context, eventID, lastError string) error {
	_, err := s.pool.Exec(ctx, `UPDATE platform_events
		SET dead_lettered_at = now(), claimed_by = '', claimed_until = NULL, last_error = $2
		WHERE id = $1`, eventID, lastError)
	return err
}

// DeliverEventToTrigger creates the task one subscriber gets from one event, and
// records that delivery, in a single transaction.
//
// The ledger's primary key is (event, subscriber), so a redelivery — after a
// worker died between creating the task and marking the event done — finds the
// row already there and reports delivered=false rather than queueing the same
// work twice. That is what turns a durable outbox into one delivery per
// subscriber instead of at least one.
func (s *Store) DeliverEventToTrigger(ctx context.Context, eventID string, trigger AgentTrigger, input CreateTaskInput) (AgentTask, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentTask{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var claimed string
	err = tx.QueryRow(ctx, `INSERT INTO event_deliveries(event_id,trigger_id) VALUES($1,$2)
		ON CONFLICT (event_id,trigger_id) DO NOTHING RETURNING event_id`, eventID, trigger.ID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already delivered to this subscriber on an earlier attempt.
		return AgentTask{}, false, nil
	}
	if err != nil {
		return AgentTask{}, false, err
	}

	scheduled := time.Now().UTC()
	if input.ScheduledAt != nil {
		scheduled = *input.ScheduledAt
	}
	if input.Priority == "" {
		input.Priority = "normal"
	}
	if input.Source == "" {
		input.Source = "event"
	}
	var task AgentTask
	err = tx.QueryRow(ctx, `INSERT INTO agent_tasks(id,agent_id,owner_id,title,input,priority,source,trigger_id,created_by,scheduled_at,deadline_at,parent_task_id,delegation_depth)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id,agent_id,owner_id,title,input,priority,status,source,trigger_id,attempts,scheduled_at,deadline_at,current_run_id,parent_task_id,delegation_depth,approval_id,last_error,created_at,updated_at`,
		uuid.NewString(), input.AgentID, input.OwnerID, input.Title, input.Input, input.Priority, input.Source, input.TriggerID, nullText(input.CreatedBy), scheduled, input.DeadlineAt, input.ParentTaskID, input.Delegation).
		Scan(&task.ID, &task.AgentID, &task.OwnerID, &task.Title, &task.Input, &task.Priority, &task.Status, &task.Source, &task.TriggerID, &task.Attempts, &task.ScheduledAt, &task.DeadlineAt, &task.CurrentRunID, &task.ParentTaskID, &task.Delegation, &task.ApprovalID, &task.LastError, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return AgentTask{}, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE event_deliveries SET task_id=$3 WHERE event_id=$1 AND trigger_id=$2`, eventID, trigger.ID, task.ID); err != nil {
		return AgentTask{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AgentTask{}, false, err
	}
	return task, true, nil
}

// TriggersForEvent returns the enabled event triggers that should see an event.
//
// The payload filter is applied in SQL with jsonb containment, so a trigger
// watching one runtime is not woken by every other runtime's events. A trigger
// never matches an event its own firing caused.
func (s *Store) TriggersForEvent(ctx context.Context, event PlatformEvent) ([]AgentTrigger, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+triggerColumns+` FROM agent_triggers
		WHERE enabled AND type='event' AND event_type=$1 AND owner_id=$2
		  AND $3::jsonb @> event_filter
		  AND ($4::text IS NULL OR id <> $4)
		ORDER BY created_at`, event.Type, event.OwnerID, event.Payload, event.CauseTriggerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

// RecentEvents backs the operator-facing event feed.
func (s *Store) RecentEvents(ctx context.Context, ownerID, eventType string, limit int) ([]PlatformEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id,e.type,e.owner_id,e.subject_type,e.subject_id,e.payload,e.cause_trigger_id,e.created_at,e.dispatched_at,
			e.attempts,e.last_error,e.dead_lettered_at,
			(SELECT count(*) FROM event_deliveries d WHERE d.event_id = e.id),
			COALESCE((SELECT string_agg(COALESCE(t.name, '삭제된 Trigger'), ', ' ORDER BY d.created_at)
				FROM event_deliveries d LEFT JOIN agent_triggers t ON t.id = d.trigger_id
				WHERE d.event_id = e.id), '')
		FROM platform_events e WHERE e.owner_id=$1 AND ($2='' OR e.type=$2)
		ORDER BY e.created_at DESC LIMIT $3`, ownerID, eventType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PlatformEvent{}
	for rows.Next() {
		var event PlatformEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.OwnerID, &event.SubjectType, &event.SubjectID,
			&event.Payload, &event.CauseTriggerID, &event.CreatedAt, &event.DispatchedAt,
			&event.Attempts, &event.LastError, &event.DeadLetteredAt, &event.Deliveries, &event.DeliveredTo); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
