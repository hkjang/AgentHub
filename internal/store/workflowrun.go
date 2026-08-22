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
// FinishWorkflowRun records the outcome and what it cost.
//
// The spend is written to its own columns as well as into the output document.
// Inside the document it is readable by whoever opens that one run; in columns it
// is countable by the usage report and by the budgets that now refuse a workflow
// when they are spent — which they could not do while the only record of a
// workflow's cost was a field in a JSON blob.
// FinishWorkflowRun records what the run produced and what it cost.
//
// The cost arrives already computed: a workflow's steps belong to different
// agents and so possibly different endpoints, so there is no single rate to apply
// here. The engine prices each step at the endpoint that answered it, which also
// means a later price correction cannot rewrite it — the same rule a run follows.
func (s *Store) FinishWorkflowRun(ctx context.Context, id, status string, output any, totalTokens, agentCalls int, cost float64, currency string) (WorkflowRun, error) {
	payload, err := json.Marshal(output)
	if err != nil {
		return WorkflowRun{}, err
	}
	var item WorkflowRun
	err = s.pool.QueryRow(ctx, `UPDATE workflow_runs SET status=$2,output=$3,total_tokens=$4,agent_calls=$5,cost=$6,currency=$7,finished_at=now() WHERE id=$1 RETURNING id,workflow_id,owner_id,status,input,output,started_at,finished_at,created_at`, id, status, payload, totalTokens, agentCalls, cost, currency).
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
