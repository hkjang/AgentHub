package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MCPToolPolicy limits which tools of one bound server an agent may call.
//
// Binding a bundle decides which servers an agent reaches, but a server is not a
// permission boundary: one MCP server commonly exposes a harmless lookup and a
// destructive write side by side.
type MCPToolPolicy struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentId"`
	ServerID   string    `json:"serverId"`
	ServerName string    `json:"serverName,omitempty"`
	Mode       string    `json:"mode"`
	Tools      []string  `json:"tools"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// PutMCPToolPolicy stores one agent's policy for one server.
func (s *Store) PutMCPToolPolicy(ctx context.Context, policy MCPToolPolicy) (MCPToolPolicy, error) {
	if policy.Mode != "allow" && policy.Mode != "deny" {
		return MCPToolPolicy{}, fmt.Errorf("알 수 없는 도구 정책 모드 %q", policy.Mode)
	}
	if policy.Tools == nil {
		policy.Tools = []string{}
	}
	if policy.ID == "" {
		policy.ID = uuid.NewString()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO mcp_tool_policies(id,agent_id,server_id,mode,tools)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(agent_id,server_id) DO UPDATE SET mode=excluded.mode,tools=excluded.tools,updated_at=now()
		RETURNING id,updated_at`, policy.ID, policy.AgentID, policy.ServerID, policy.Mode, policy.Tools).
		Scan(&policy.ID, &policy.UpdatedAt)
	return policy, err
}

// MCPToolPolicies returns an agent's policies, named for the console.
func (s *Store) MCPToolPolicies(ctx context.Context, agentID string) ([]MCPToolPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id,p.agent_id,p.server_id,m.name,p.mode,p.tools,p.updated_at
		FROM mcp_tool_policies p JOIN mcp_servers m ON m.id=p.server_id
		WHERE p.agent_id=$1 ORDER BY m.name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MCPToolPolicy{}
	for rows.Next() {
		var item MCPToolPolicy
		if err := rows.Scan(&item.ID, &item.AgentID, &item.ServerID, &item.ServerName, &item.Mode, &item.Tools, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteMCPToolPolicy removes a policy, restoring the bundle binding as the only
// restriction on that server.
func (s *Store) DeleteMCPToolPolicy(ctx context.Context, id, ownerID string) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM mcp_tool_policies p USING agent_definitions a
		WHERE p.id=$1 AND p.agent_id=a.id AND a.owner_id=$2`, id, ownerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
