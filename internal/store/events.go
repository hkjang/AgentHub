package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event types the platform publishes. Keeping them as constants means a trigger
// can be validated against the list instead of silently never firing.
const (
	EventTaskCompleted    = "task.completed"
	EventTaskFailed       = "task.failed"
	EventTaskDeadLettered = "task.dead_lettered"
	EventApprovalDecided  = "approval.decided"
	EventRuntimeFailed    = "runtime.failed"
	EventArtifactCreated  = "artifact.created"
)

// PublishableEvents is the list an operator may subscribe a trigger to.
var PublishableEvents = []string{
	EventTaskCompleted, EventTaskFailed, EventTaskDeadLettered,
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
	DispatchedAt   *time.Time      `json:"dispatchedAt,omitempty"`
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

// ClaimEvents takes a batch of undelivered events and marks them dispatched in
// the same statement, so two workers cannot deliver the same event twice.
func (s *Store) ClaimEvents(ctx context.Context, limit int) ([]PlatformEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		WITH claimed AS (
			SELECT id FROM platform_events
			WHERE dispatched_at IS NULL
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE platform_events e SET dispatched_at = now()
		FROM claimed WHERE e.id = claimed.id
		RETURNING e.id, e.type, e.owner_id, e.subject_type, e.subject_id, e.payload, e.cause_trigger_id, e.created_at`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PlatformEvent{}
	for rows.Next() {
		var event PlatformEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.OwnerID, &event.SubjectType, &event.SubjectID,
			&event.Payload, &event.CauseTriggerID, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
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
	rows, err := s.pool.Query(ctx, `SELECT id,type,owner_id,subject_type,subject_id,payload,cause_trigger_id,created_at,dispatched_at
		FROM platform_events WHERE owner_id=$1 AND ($2='' OR type=$2)
		ORDER BY created_at DESC LIMIT $3`, ownerID, eventType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PlatformEvent{}
	for rows.Next() {
		var event PlatformEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.OwnerID, &event.SubjectType, &event.SubjectID,
			&event.Payload, &event.CauseTriggerID, &event.CreatedAt, &event.DispatchedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
