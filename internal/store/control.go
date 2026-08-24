package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AgentPlan is what a run intended to do, kept alongside the record of what it
// actually did.
type AgentPlan struct {
	ID        string          `json:"id"`
	RunID     string          `json:"runId"`
	TaskID    string          `json:"taskId"`
	Mode      string          `json:"mode"`
	Goal      string          `json:"goal"`
	Steps     json.RawMessage `json:"steps"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (s *Store) CreatePlan(ctx context.Context, plan AgentPlan) (AgentPlan, error) {
	if plan.ID == "" {
		plan.ID = uuid.NewString()
	}
	if len(plan.Steps) == 0 {
		plan.Steps = json.RawMessage(`[]`)
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_plans(id,run_id,task_id,mode,goal,steps) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at`,
		plan.ID, plan.RunID, plan.TaskID, plan.Mode, plan.Goal, plan.Steps).Scan(&plan.CreatedAt)
	return plan, err
}

func (s *Store) PlanForRun(ctx context.Context, runID string) (AgentPlan, error) {
	var plan AgentPlan
	err := s.pool.QueryRow(ctx, `SELECT id,run_id,task_id,mode,goal,steps,created_at FROM agent_plans WHERE run_id=$1 ORDER BY created_at LIMIT 1`, runID).
		Scan(&plan.ID, &plan.RunID, &plan.TaskID, &plan.Mode, &plan.Goal, &plan.Steps, &plan.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPlan{}, ErrNotFound
	}
	return plan, err
}

// AgentMemory is something the agent remembers across runs. It lives here rather
// than in the Runtime's home directory so that it survives the Pod.
type AgentMemory struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"ownerId"`
	Scope       string    `json:"scope"`
	AgentID     *string   `json:"agentId,omitempty"`
	TaskID      *string   `json:"taskId,omitempty"`
	WorkspaceID *string   `json:"workspaceId,omitempty"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// maxMemoryValueBytes keeps one remembered fact from crowding out the prompt.
const maxMemoryValueBytes = 4000

// PutMemory writes or replaces one entry. The unique index per scope means a
// rewrite replaces rather than accumulating duplicates of the same key.
func (s *Store) PutMemory(ctx context.Context, item AgentMemory, runID string) error {
	if len(item.Value) > maxMemoryValueBytes {
		return Invalid{Message: fmt.Sprintf("기억 %q의 값이 %d바이트로 상한(%d)을 넘습니다", item.Key, len(item.Value), maxMemoryValueBytes)}
	}
	var conflict string
	switch item.Scope {
	case "agent":
		conflict = "(agent_id, key) WHERE scope = 'agent'"
	case "task":
		conflict = "(task_id, key) WHERE scope = 'task'"
	case "workspace":
		conflict = "(workspace_id, key) WHERE scope = 'workspace'"
	default:
		return Invalid{Message: fmt.Sprintf("알 수 없는 기억 범위 %q", item.Scope)}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_memories(id,owner_id,scope,agent_id,task_id,workspace_id,key,value,written_by_run_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT `+conflict+` DO UPDATE SET value=excluded.value,written_by_run_id=excluded.written_by_run_id,updated_at=now()`,
		uuid.NewString(), item.OwnerID, item.Scope, item.AgentID, item.TaskID, item.WorkspaceID, item.Key, item.Value, nullText(runID))
	return err
}

// Memories returns what an agent should be given at the start of a run: its own
// long-term entries plus anything shared through its workspace.
func (s *Store) Memories(ctx context.Context, agentID string) ([]AgentMemory, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,owner_id,scope,agent_id,task_id,workspace_id,key,value,updated_at FROM agent_memories
		WHERE (scope='agent' AND agent_id=$1)
		   OR (scope='workspace' AND workspace_id = (SELECT workspace_id FROM agent_definitions WHERE id=$1))
		ORDER BY scope, key`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AgentMemory{}
	for rows.Next() {
		var item AgentMemory
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Scope, &item.AgentID, &item.TaskID, &item.WorkspaceID, &item.Key, &item.Value, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteMemory(ctx context.Context, id, ownerID string) error {
	return s.deleteScoped(ctx, "agent_memories", id, ownerID, false, "memory")
}

// ParkTaskForApproval moves a task out of the queue while a human decides. It is
// not a failure, so it must not consume a retry.
func (s *Store) ParkTaskForApproval(ctx context.Context, taskID, approvalID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status='waiting_approval',approval_id=$2,claimed_by='',claimed_until=NULL,updated_at=now()
		WHERE id=$1 AND status<>'cancelled'`, taskID, approvalID)
	return err
}

// ResumeApprovedTask puts a decided task back on the queue.
//
// The attempt counter is rolled back because waiting for a person is not a
// failed attempt; without this a long approval would eat the retry budget.
func (s *Store) ResumeApprovedTask(ctx context.Context, approvalID string) (AgentTask, error) {
	var taskID string
	err := s.pool.QueryRow(ctx, `UPDATE agent_tasks SET status='queued',scheduled_at=now(),attempts=GREATEST(attempts-1,0),updated_at=now()
		WHERE approval_id=$1 AND status='waiting_approval' RETURNING id`, approvalID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	if err != nil {
		return AgentTask{}, err
	}
	return s.AgentTaskByIDUnscoped(ctx, taskID)
}

// FailRejectedTask ends a task whose approval was refused. A refusal is a
// decision, not an error, so it is never retried.
func (s *Store) FailRejectedTask(ctx context.Context, approvalID, reason string) (AgentTask, error) {
	var taskID string
	err := s.pool.QueryRow(ctx, `UPDATE agent_tasks SET status='failed',last_error=$2,updated_at=now()
		WHERE approval_id=$1 AND status='waiting_approval' RETURNING id`, approvalID, reason).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	if err != nil {
		return AgentTask{}, err
	}
	return s.AgentTaskByIDUnscoped(ctx, taskID)
}

// AgentTaskByIDUnscoped reads a task without an ownership filter. It is for
// server-side flows such as an approval decision, where the reviewer is
// deliberately not the task's owner.
func (s *Store) AgentTaskByIDUnscoped(ctx context.Context, id string) (AgentTask, error) {
	item, err := scanTask(s.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM agent_tasks t JOIN agent_definitions a ON a.id=t.agent_id WHERE t.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentTask{}, ErrNotFound
	}
	return item, err
}

// ApprovalDecisionForTask reports how a parked task's approval was decided, so a
// resumed run can tell the agent what the reviewer said.
func (s *Store) ApprovalDecisionForTask(ctx context.Context, taskID string) (string, string, error) {
	var status, reason string
	err := s.pool.QueryRow(ctx, `SELECT a.status, a.reason FROM approvals a JOIN agent_tasks t ON t.approval_id=a.id WHERE t.id=$1`, taskID).Scan(&status, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return status, reason, err
}

// AgentByName resolves a delegation target within one owner's agents. Delegation
// is by name because that is what an agent can reasonably produce.
func (s *Store) AgentByName(ctx context.Context, ownerID, name string) (Agent, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM agent_definitions WHERE owner_id=$1 AND lower(name)=lower($2)`, ownerID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	return s.AgentByID(ctx, id, ownerID, false)
}

// DelegationChain walks a task's ancestry, newest first. The orchestrator uses it
// to reject a cycle such as A → B → C → A before the child task is created.
func (s *Store) DelegationChain(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_task_id, agent_id, 0 AS depth FROM agent_tasks WHERE id=$1
			UNION ALL
			SELECT t.id, t.parent_task_id, t.agent_id, chain.depth+1
			FROM agent_tasks t JOIN chain ON chain.parent_task_id = t.id
			WHERE chain.depth < 20
		)
		SELECT agent_id FROM chain ORDER BY depth`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []string{}
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return nil, err
		}
		agents = append(agents, agentID)
	}
	return agents, rows.Err()
}
