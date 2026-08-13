package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type EvaluationTestSet struct {
	ID            string          `json:"id"`
	OwnerID       string          `json:"ownerId"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	PassThreshold int             `json:"passThreshold"`
	Cases         json.RawMessage `json:"cases"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type AgentEvaluation struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agentId"`
	AgentName   string          `json:"agentName"`
	TestSetID   string          `json:"testSetId"`
	TestSetName string          `json:"testSetName"`
	Status      string          `json:"status"`
	Score       int             `json:"score"`
	Metrics     json.RawMessage `json:"metrics"`
	Result      json.RawMessage `json:"result"`
	CreatedAt   time.Time       `json:"createdAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}

func (s *Store) EvaluationTestSets(ctx context.Context, ownerID string) ([]EvaluationTestSet, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,owner_id,name,description,pass_threshold,cases,created_at,updated_at FROM evaluation_test_sets WHERE owner_id=$1 ORDER BY updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EvaluationTestSet{}
	for rows.Next() {
		var item EvaluationTestSet
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.PassThreshold, &item.Cases, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertEvaluationTestSet(ctx context.Context, ownerID string, item EvaluationTestSet) (EvaluationTestSet, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	item.OwnerID = ownerID
	err := s.pool.QueryRow(ctx, `INSERT INTO evaluation_test_sets(id,owner_id,name,description,pass_threshold,cases) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,pass_threshold=excluded.pass_threshold,cases=excluded.cases,updated_at=now() WHERE evaluation_test_sets.owner_id=excluded.owner_id RETURNING id,owner_id,name,description,pass_threshold,cases,created_at,updated_at`, item.ID, ownerID, item.Name, item.Description, item.PassThreshold, item.Cases).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.PassThreshold, &item.Cases, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *Store) EvaluationTestSetByID(ctx context.Context, ownerID, id string) (EvaluationTestSet, error) {
	var item EvaluationTestSet
	err := s.pool.QueryRow(ctx, `SELECT id,owner_id,name,description,pass_threshold,cases,created_at,updated_at FROM evaluation_test_sets WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description, &item.PassThreshold, &item.Cases, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *Store) CreateAgentEvaluation(ctx context.Context, ownerID, agentID, testSetID, status string, score int, metrics, result any) (AgentEvaluation, error) {
	id := uuid.NewString()
	metricsJSON, _ := json.Marshal(metrics)
	resultJSON, _ := json.Marshal(result)
	var item AgentEvaluation
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_evaluations(id,owner_id,agent_id,test_set_id,status,score,metrics,result,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()) RETURNING id,agent_id,test_set_id,status,score,metrics,result,created_at,completed_at`, id, ownerID, agentID, testSetID, status, score, metricsJSON, resultJSON).Scan(&item.ID, &item.AgentID, &item.TestSetID, &item.Status, &item.Score, &item.Metrics, &item.Result, &item.CreatedAt, &item.CompletedAt)
	return item, err
}

func (s *Store) AgentEvaluations(ctx context.Context, ownerID string) ([]AgentEvaluation, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.id,e.agent_id,a.name,e.test_set_id,t.name,e.status,e.score,e.metrics,e.result,e.created_at,e.completed_at FROM agent_evaluations e JOIN agent_definitions a ON a.id=e.agent_id JOIN evaluation_test_sets t ON t.id=e.test_set_id WHERE e.owner_id=$1 ORDER BY e.created_at DESC LIMIT 200`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentEvaluation{}
	for rows.Next() {
		var item AgentEvaluation
		if err := rows.Scan(&item.ID, &item.AgentID, &item.AgentName, &item.TestSetID, &item.TestSetName, &item.Status, &item.Score, &item.Metrics, &item.Result, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
