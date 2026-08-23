package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// What the execution fabric did, kept beside the run.
//
// The ids are Orca's, stored verbatim rather than translated: they are what
// `orca orchestration dispatch-show --task <id>` takes, so a record here can be
// checked against the fabric that produced it. A provenance record that cannot
// be looked up in the system it describes is a claim, not evidence.

// OrcaDispatch is one task handed to the fabric.
type OrcaDispatch struct {
	ID         string    `json:"id"`
	RunID      string    `json:"runId"`
	OrcaRunID  string    `json:"orcaRunId"`
	OrcaTaskID string    `json:"orcaTaskId"`
	Terminal   string    `json:"terminal"`
	Worktree   string    `json:"worktree"`
	Branch     string    `json:"branch"`
	Role       string    `json:"role"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	CreatedAt  time.Time `json:"createdAt"`
}

// SaveOrcaDispatch records one hand-off to the fabric.
func (s *Store) SaveOrcaDispatch(ctx context.Context, item OrcaDispatch) error {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO orca_dispatches
		(id,run_id,orca_run_id,orca_task_id,terminal,worktree,branch,role,status,detail)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		item.ID, item.RunID, item.OrcaRunID, item.OrcaTaskID, item.Terminal,
		item.Worktree, item.Branch, item.Role, item.Status, item.Detail)
	return err
}

// OrcaDispatches lists what the fabric did for one run.
func (s *Store) OrcaDispatches(ctx context.Context, runID string) ([]OrcaDispatch, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,run_id,orca_run_id,orca_task_id,terminal,worktree,branch,role,status,detail,created_at
		FROM orca_dispatches WHERE run_id=$1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []OrcaDispatch{}
	for rows.Next() {
		var item OrcaDispatch
		if err := rows.Scan(&item.ID, &item.RunID, &item.OrcaRunID, &item.OrcaTaskID, &item.Terminal,
			&item.Worktree, &item.Branch, &item.Role, &item.Status, &item.Detail, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
