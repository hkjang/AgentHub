package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/cryptox"
)

// Approval used to be something the agent volunteered: its goal asked it to
// declare a state-changing action and wait, so an agent that simply called the
// tool went around the gate. This is the other half — the in-Pod egress gateway
// holds a gated call until a person decides, and asks the control plane through
// here.
//
// The gateway authenticates with the runtime's own token. Only its hash is kept:
// the token lives in the Pod's Secret, and a database copy would be a second
// place to steal it from.

// ToolApproval is one gated tool call waiting for a decision.
type ToolApproval struct {
	ID         string    `json:"id"`
	ApprovalID string    `json:"approvalId"`
	RuntimeID  string    `json:"runtimeId"`
	AgentID    string    `json:"agentId"`
	OwnerID    string    `json:"ownerId"`
	ServerName string    `json:"serverName"`
	ToolName   string    `json:"toolName"`
	Arguments  string    `json:"arguments"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
}

// SetRuntimeGatewayToken records the hash of the token the Pod was given, which
// is what lets the control plane recognise a request from that Pod's gateway.
func (s *Store) SetRuntimeGatewayToken(ctx context.Context, runtimeID, token string) error {
	if runtimeID == "" || token == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_runtimes SET gateway_token_hash=$2, updated_at=now() WHERE id=$1`, runtimeID, cryptox.TokenHash(token))
	return err
}

// RuntimeByGatewayToken identifies the runtime a gateway request came from.
//
// The token is compared by hash, and the lookup is the authentication: a caller
// that cannot present the token of a runtime that exists gets nothing.
func (s *Store) RuntimeByGatewayToken(ctx context.Context, token string) (Runtime, error) {
	if token == "" {
		return Runtime{}, ErrNotFound
	}
	var item Runtime
	err := s.pool.QueryRow(ctx, `SELECT id,agent_id,owner_id,status,desired_state,crd_name,pod_name,node_name,endpoint,restart_count,failure_reason,last_activity_at,started_at,stopped_at,warm_until,created_at,updated_at
		FROM agent_runtimes WHERE gateway_token_hash=$1`, cryptox.TokenHash(token)).
		Scan(&item.ID, &item.AgentID, &item.OwnerID, &item.Status, &item.DesiredState, &item.CRDName, &item.PodName, &item.NodeName, &item.Endpoint, &item.RestartCount, &item.FailureReason, &item.LastActivityAt, &item.StartedAt, &item.StoppedAt, &item.WarmUntil, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrNotFound
	}
	return item, err
}

// CreateToolApproval records a gated call and the approval a reviewer decides on.
//
// Both rows are written in one transaction: an approval with no call attached
// would be undecidable, and a call with no approval would wait forever.
func (s *Store) CreateToolApproval(ctx context.Context, item ToolApproval, reason string) (ToolApproval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ToolApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	approvalID := uuid.NewString()
	payload := map[string]any{
		"server": item.ServerName, "tool": item.ToolName, "arguments": item.Arguments,
		"agentId": item.AgentID, "runtimeId": item.RuntimeID,
	}
	raw, _ := json.Marshal(payload)
	var approval Approval
	err = tx.QueryRow(ctx, `INSERT INTO approvals(id,requester_id,reviewer_id,resource_type,resource_id,action,reason,payload)
		SELECT $1,$2,manager_id,'tool',$3,$4,$5,$6 FROM users WHERE id=$2
		RETURNING id,status,created_at`,
		approvalID, item.OwnerID, item.RuntimeID, "tool.call:"+item.ServerName+"/"+item.ToolName, reason, raw).
		Scan(&approval.ID, &approval.Status, &approval.CreatedAt)
	if err != nil {
		return ToolApproval{}, err
	}

	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	err = tx.QueryRow(ctx, `INSERT INTO tool_approvals(id,approval_id,runtime_id,agent_id,owner_id,server_name,tool_name,arguments)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`,
		item.ID, approval.ID, item.RuntimeID, item.AgentID, item.OwnerID, item.ServerName, item.ToolName, item.Arguments).
		Scan(&item.CreatedAt)
	if err != nil {
		return ToolApproval{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ToolApproval{}, err
	}
	item.ApprovalID = approval.ID
	item.Status = approval.Status
	return item, nil
}

// ToolApprovalStatus is what the waiting gateway polls for. It is scoped to the
// runtime that asked, so one runtime's token cannot read another's decisions.
func (s *Store) ToolApprovalStatus(ctx context.Context, id, runtimeID string) (ToolApproval, error) {
	var item ToolApproval
	err := s.pool.QueryRow(ctx, `SELECT t.id,t.approval_id,t.runtime_id,t.agent_id,t.owner_id,t.server_name,t.tool_name,t.arguments,a.status,t.created_at
		FROM tool_approvals t JOIN approvals a ON a.id = t.approval_id
		WHERE t.id=$1 AND t.runtime_id=$2`, id, runtimeID).
		Scan(&item.ID, &item.ApprovalID, &item.RuntimeID, &item.AgentID, &item.OwnerID, &item.ServerName, &item.ToolName, &item.Arguments, &item.Status, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolApproval{}, ErrNotFound
	}
	return item, err
}
