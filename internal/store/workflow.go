package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Workflow struct {
	ID                 string          `json:"id"`
	OwnerID            string          `json:"ownerId"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	Mode               string          `json:"mode"`
	MaxDepth           int             `json:"maxDepth"`
	MaxAgentCalls      int             `json:"maxAgentCalls"`
	MaxToolCalls       int             `json:"maxToolCalls"`
	MaxDurationSeconds int             `json:"maxDurationSeconds"`
	MaxParallelAgents  int             `json:"maxParallelAgents"`
	Definition         json.RawMessage `json:"definition"`
	Enabled            bool            `json:"enabled"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

func (s *Store) Workflows(ctx context.Context, ownerID string) ([]Workflow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,owner_id,name,description,mode,max_depth,max_agent_calls,max_tool_calls,max_duration_seconds,max_parallel_agents,definition,enabled,created_at,updated_at FROM agent_workflows WHERE owner_id=$1 ORDER BY updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Workflow{}
	for rows.Next() {
		var item Workflow
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.Mode, &item.MaxDepth, &item.MaxAgentCalls, &item.MaxToolCalls, &item.MaxDurationSeconds, &item.MaxParallelAgents, &item.Definition, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) WorkflowByID(ctx context.Context, ownerID, id string) (Workflow, error) {
	var item Workflow
	err := s.pool.QueryRow(ctx, `SELECT id,owner_id,name,description,mode,max_depth,max_agent_calls,max_tool_calls,max_duration_seconds,max_parallel_agents,definition,enabled,created_at,updated_at FROM agent_workflows WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.Mode, &item.MaxDepth, &item.MaxAgentCalls, &item.MaxToolCalls, &item.MaxDurationSeconds, &item.MaxParallelAgents, &item.Definition, &item.Enabled, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *Store) UpsertWorkflow(ctx context.Context, ownerID string, item Workflow) (Workflow, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	item.OwnerID = ownerID
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_workflows(id,owner_id,name,description,mode,max_depth,max_agent_calls,max_tool_calls,max_duration_seconds,max_parallel_agents,definition,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,mode=excluded.mode,max_depth=excluded.max_depth,max_agent_calls=excluded.max_agent_calls,max_tool_calls=excluded.max_tool_calls,max_duration_seconds=excluded.max_duration_seconds,max_parallel_agents=excluded.max_parallel_agents,definition=excluded.definition,enabled=excluded.enabled,updated_at=now() WHERE agent_workflows.owner_id=excluded.owner_id RETURNING id,owner_id,name,description,mode,max_depth,max_agent_calls,max_tool_calls,max_duration_seconds,max_parallel_agents,definition,enabled,created_at,updated_at`, item.ID, ownerID, item.Name, item.Description, item.Mode, item.MaxDepth, item.MaxAgentCalls, item.MaxToolCalls, item.MaxDurationSeconds, item.MaxParallelAgents, item.Definition, item.Enabled).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.Mode, &item.MaxDepth, &item.MaxAgentCalls, &item.MaxToolCalls, &item.MaxDurationSeconds, &item.MaxParallelAgents, &item.Definition, &item.Enabled, &item.CreatedAt, &item.UpdatedAt)
	err = conflictIfTaken(err, "같은 이름의 워크플로가 이미 있습니다. 다른 이름을 쓰세요")
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}
