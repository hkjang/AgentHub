package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WorkflowRun struct {
	ID         string          `json:"id"`
	WorkflowID string          `json:"workflowId"`
	OwnerID    string          `json:"ownerId"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

func (s *Store) CreateWorkflowRun(ctx context.Context, workflowID, ownerID string, input any) (WorkflowRun, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return WorkflowRun{}, err
	}
	var item WorkflowRun
	err = s.pool.QueryRow(ctx, `INSERT INTO workflow_runs(id,workflow_id,owner_id,status,input,started_at) VALUES($1,$2,$3,'running',$4,now()) RETURNING id,workflow_id,owner_id,status,input,output,started_at,finished_at,created_at`,
		uuid.NewString(), workflowID, ownerID, payload).
		Scan(&item.ID, &item.WorkflowID, &item.OwnerID, &item.Status, &item.Input, &item.Output, &item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	return item, err
}

// FinishWorkflowRun records the terminal state. The output carries the per-step
// trace so a completed run stays auditable without re-running it.
func (s *Store) FinishWorkflowRun(ctx context.Context, id, status string, output any) (WorkflowRun, error) {
	payload, err := json.Marshal(output)
	if err != nil {
		return WorkflowRun{}, err
	}
	var item WorkflowRun
	err = s.pool.QueryRow(ctx, `UPDATE workflow_runs SET status=$2,output=$3,finished_at=now() WHERE id=$1 RETURNING id,workflow_id,owner_id,status,input,output,started_at,finished_at,created_at`, id, status, payload).
		Scan(&item.ID, &item.WorkflowID, &item.OwnerID, &item.Status, &item.Input, &item.Output, &item.StartedAt, &item.FinishedAt, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkflowRun{}, ErrNotFound
	}
	return item, err
}

func (s *Store) WorkflowRuns(ctx context.Context, ownerID, workflowID string) ([]WorkflowRun, error) {
	query := `SELECT id,workflow_id,owner_id,status,input,output,started_at,finished_at,created_at FROM workflow_runs WHERE owner_id=$1`
	args := []any{ownerID}
	if workflowID != "" {
		query += ` AND workflow_id=$2`
		args = append(args, workflowID)
	}
	query += ` ORDER BY created_at DESC LIMIT 50`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WorkflowRun{}
	for rows.Next() {
		var item WorkflowRun
		if err := rows.Scan(&item.ID, &item.WorkflowID, &item.OwnerID, &item.Status, &item.Input, &item.Output, &item.StartedAt, &item.FinishedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
