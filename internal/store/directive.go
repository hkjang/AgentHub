package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Saying something to a run that is already going.
//
// The person is at a browser and the conversation is held by a worker process
// somewhere else, so this table is the path between them. Nothing here delivers
// anything: it records what somebody said and, separately, whether it was said.
// Those are different facts, and a console that showed the first as the second
// would be claiming the agent had heard.

// RunDirective is one thing to say to a running agent.
type RunDirective struct {
	ID          string     `json:"id"`
	RunID       string     `json:"runId"`
	Kind        string     `json:"kind"`
	Message     string     `json:"message"`
	CreatedBy   *string    `json:"createdBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
}

// RunDirectiveKinds are what a person may say. Aborting is not among them: a
// task is cancelled through the task, which already stops the run, and a second
// way to stop the same work would be a second thing to keep in step.
var RunDirectiveKinds = []string{"steer", "follow_up"}

// AddRunDirective records something to say to a run.
//
// The run has to be one that is still going and one this person may reach.
// Saying something into a finished run would be accepted and never delivered,
// which reads as the platform ignoring them.
func (s *Store) AddRunDirective(ctx context.Context, runID, ownerID, kind, message string, admin bool) (RunDirective, error) {
	if !contains(RunDirectiveKinds, kind) {
		return RunDirective{}, Invalid{Message: "알 수 없는 지시 종류입니다: " + kind}
	}
	item := RunDirective{ID: uuid.NewString(), RunID: runID, Kind: kind, Message: message}
	query := `INSERT INTO run_directives (id, run_id, kind, message, created_by)
		SELECT $1, r.id, $2, $3, $4 FROM agent_runs r
		WHERE r.id=$5 AND r.finished_at IS NULL`
	args := []any{item.ID, kind, message, ownerID, runID}
	if !admin {
		query += ` AND r.owner_id=$6`
		args = append(args, ownerID)
	}
	query += ` RETURNING id, run_id, kind, message, created_by, created_at, delivered_at, outcome`
	err := s.pool.QueryRow(ctx, query, args...).Scan(&item.ID, &item.RunID, &item.Kind, &item.Message,
		&item.CreatedBy, &item.CreatedAt, &item.DeliveredAt, &item.Outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the run is not this person's or it has already finished. The two
		// are not distinguished on purpose: which run belongs to whom is not
		// something an unrelated person should learn by asking.
		return RunDirective{}, ErrNotFound
	}
	return item, err
}

// TakeRunDirectives claims what nobody has delivered yet.
//
// Claimed rather than read: the row is marked in the same statement that returns
// it, so a worker that reads twice does not say the same thing twice. Being told
// "actually, do it the other way" a second time is worse than not being told at
// all, because the agent acts on it again.
func (s *Store) TakeRunDirectives(ctx context.Context, runID string) ([]RunDirective, error) {
	rows, err := s.pool.Query(ctx, `UPDATE run_directives SET delivered_at=now()
		WHERE id IN (SELECT id FROM run_directives WHERE run_id=$1 AND delivered_at IS NULL ORDER BY created_at FOR UPDATE SKIP LOCKED)
		RETURNING id, run_id, kind, message, created_by, created_at, delivered_at, outcome`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RunDirective{}
	for rows.Next() {
		var item RunDirective
		if err := rows.Scan(&item.ID, &item.RunID, &item.Kind, &item.Message, &item.CreatedBy,
			&item.CreatedAt, &item.DeliveredAt, &item.Outcome); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecordDirectiveOutcome keeps what the agent said back.
//
// A directive the agent refused is not a directive that was delivered, whatever
// the timestamp says, and the person who sent it is the one who needs to know.
func (s *Store) RecordDirectiveOutcome(ctx context.Context, id, outcome string) error {
	_, err := s.pool.Exec(ctx, `UPDATE run_directives SET outcome=$2 WHERE id=$1`, id, outcome)
	return err
}

// RunDirectives lists what has been said to one run, in order.
func (s *Store) RunDirectives(ctx context.Context, runID string) ([]RunDirective, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, run_id, kind, message, created_by, created_at, delivered_at, outcome
		FROM run_directives WHERE run_id=$1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RunDirective{}
	for rows.Next() {
		var item RunDirective
		if err := rows.Scan(&item.ID, &item.RunID, &item.Kind, &item.Message, &item.CreatedBy,
			&item.CreatedAt, &item.DeliveredAt, &item.Outcome); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
