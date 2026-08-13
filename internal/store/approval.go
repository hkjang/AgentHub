package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Approval struct {
	ID            string          `json:"id"`
	RequesterID   string          `json:"requesterId"`
	RequesterName string          `json:"requesterName"`
	ReviewerID    *string         `json:"reviewerId,omitempty"`
	ResourceType  string          `json:"resourceType"`
	ResourceID    string          `json:"resourceId"`
	Action        string          `json:"action"`
	Reason        string          `json:"reason"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	DecidedAt     *time.Time      `json:"decidedAt,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (s *Store) CreateApproval(ctx context.Context, requesterID, resourceType, resourceID, action, reason string, payload any) (Approval, error) {
	raw, _ := json.Marshal(payload)
	id := uuid.NewString()
	var a Approval
	err := s.pool.QueryRow(ctx, `INSERT INTO approvals(id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload) SELECT $1,$2,manager_id,$3,$4,$5,$6,$7 FROM users WHERE id=$2 RETURNING id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload,status,decided_at,created_at`, id, requesterID, resourceType, resourceID, action, reason, raw).Scan(&a.ID, &a.RequesterID, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt)
	return a, err
}
func (s *Store) Approvals(ctx context.Context, reviewerID string, admin bool) ([]Approval, error) {
	query := `SELECT a.id,a.requester_id,u.username,a.reviewer_id,a.resource_type,a.resource_id,a.action,a.reason,a.payload,a.status,a.decided_at,a.created_at FROM approvals a JOIN users u ON u.id=a.requester_id`
	args := []any{}
	if !admin {
		query += ` WHERE a.reviewer_id=$1`
		args = append(args, reviewerID)
	}
	query += ` ORDER BY CASE WHEN a.status='pending' THEN 0 ELSE 1 END,a.created_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.RequesterID, &a.RequesterName, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *Store) DecideApproval(ctx context.Context, id, reviewerID, decision string, admin bool) (Approval, error) {
	var a Approval
	err := s.pool.QueryRow(ctx, `UPDATE approvals SET status=$1,reviewer_id=$2,decided_at=now() WHERE id=$3 AND status='pending' AND ($4 OR reviewer_id=$2) RETURNING id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload,status,decided_at,created_at`, decision, reviewerID, id, admin).Scan(&a.ID, &a.RequesterID, &a.ReviewerID, &a.ResourceType, &a.ResourceID, &a.Action, &a.Reason, &a.Payload, &a.Status, &a.DecidedAt, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	return a, err
}
