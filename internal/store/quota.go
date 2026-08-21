package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Execution quotas: what a user may run, not only what they may hold.
//
// The resource quotas above bound runtimes, CPU, memory and storage. These bound
// the thing that actually costs money when nobody is watching — tokens — and the
// thing one person can monopolise: worker slots.

// QuotaWindow is how far back spend is counted. It matches the console's usage
// report, so the number in a refusal is the number the operator can go and look
// at rather than a second, differently-scoped total.
const QuotaWindow = 30 * 24 * time.Hour

// ExecutionPolicy reads the configured limits. A deployment that never set them
// gets zeroes, which mean unlimited.
func (s *Store) ExecutionPolicy(ctx context.Context) (quota.Policy, error) {
	var policy quota.Policy
	if err := s.Setting(ctx, "governance", &policy); err != nil {
		if err == ErrNotFound {
			return quota.Policy{}, nil
		}
		return quota.Policy{}, err
	}
	return policy, nil
}

// ExecutionPolicyFor is the policy that applies to one person, after their
// department's per-member limits and their own override. The platform-wide
// policy above is what everybody gets when neither is set.
//
// It exists so the enqueue path stops asking a question the platform can no
// longer answer on its own: "how many tasks may a user run" now depends on which
// user.
func (s *Store) ExecutionPolicyFor(ctx context.Context, userID string) (quota.Policy, error) {
	resolved, err := s.ResolveQuota(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return s.ExecutionPolicy(ctx)
		}
		return quota.Policy{}, err
	}
	return quota.Policy{
		MaxRunningTasksPerUser: resolved.Effective.MaxRunningTasks,
		TokenBudgetPerUser:     resolved.Effective.TokenBudget,
		CostBudgetPerUser:      resolved.Effective.CostBudget,
	}, nil
}

// ExecutionUsage measures one owner against those limits: how many of their tasks
// are running right now, and what their agents have spent in the window.
//
// exceptTaskID is the task being decided, which has already been claimed and must
// not count against its own limit.
func (s *Store) ExecutionUsage(ctx context.Context, ownerID, agentID, exceptTaskID string) (quota.Usage, error) {
	return executionUsage(ctx, s.pool, ownerID, agentID, exceptTaskID)
}

// querier is the part of a pool or a transaction this file needs.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func executionUsage(ctx context.Context, db querier, ownerID, agentID, exceptTaskID string) (quota.Usage, error) {
	usage := quota.Usage{Currency: "KRW"}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM agent_tasks
		WHERE owner_id=$1 AND status IN ('running','planning','ready','waiting_tool') AND id <> $2`,
		ownerID, exceptTaskID).Scan(&usage.RunningTasks); err != nil {
		return usage, err
	}
	since := time.Now().UTC().Add(-QuotaWindow)
	err := db.QueryRow(ctx, `
		SELECT COALESCE(sum(s.prompt_tokens + s.completion_tokens), 0),
		       COALESCE(sum(`+usageCostSQL+`), 0),
		       COALESCE(sum(CASE WHEN r.agent_id = $3 THEN s.prompt_tokens + s.completion_tokens ELSE 0 END), 0)
		FROM agent_run_steps s
		JOIN agent_runs r ON r.id = s.run_id
		LEFT JOIN model_endpoints m ON m.id = r.model_endpoint_id
		WHERE r.owner_id = $1 AND s.created_at >= $2`,
		ownerID, since, agentID).Scan(&usage.Tokens, &usage.Cost, &usage.AgentTokens)
	return usage, err
}

// ReserveExecutionSlot decides whether one claimed task may run, and puts it back
// if it may not — both under a lock held for that owner.
//
// The decision cannot be made by counting alone. Two tasks claimed in the same
// instant each see the other running, so with a limit of one both would step
// aside and neither would run. Under pg_advisory_xact_lock the decisions for one
// owner are serialised, and a task that stands down does so in the same
// transaction it was counted in — so the next decision sees a queued task rather
// than a running one, and exactly as many tasks run as the limit allows.
func (s *Store) ReserveExecutionSlot(ctx context.Context, task AgentTask, policy quota.Policy, agentBudget int64, wait time.Duration) (quota.Decision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return quota.Decision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// hashtext gives one lock per owner; a different owner's decisions never wait
	// on this one.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, task.OwnerID); err != nil {
		return quota.Decision{}, err
	}
	usage, err := executionUsage(ctx, tx, task.OwnerID, task.AgentID, task.ID)
	if err != nil {
		return quota.Decision{}, err
	}
	decision := quota.Evaluate(policy, agentBudget, usage)
	if decision.Wait {
		if _, err := tx.Exec(ctx, deferTaskSQL, task.ID, wait.String(), decision.Reason); err != nil {
			return quota.Decision{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return quota.Decision{}, err
	}
	return decision, nil
}

// deferTaskSQL is shared by the reservation and the standalone defer.
const deferTaskSQL = `UPDATE agent_tasks
	SET status='queued', scheduled_at=now() + $2::interval, attempts=GREATEST(attempts - 1, 0),
	    claimed_by='', claimed_until=NULL, last_error=$3, updated_at=now()
	WHERE id=$1`

// DeferAgentTask puts a claimed task back on the queue without spending an
// attempt.
//
// Waiting for a free slot is not a failed attempt: counting it would let a busy
// hour exhaust a task's retry budget before it ever ran.
func (s *Store) DeferAgentTask(ctx context.Context, taskID string, delay time.Duration, reason string) error {
	_, err := s.pool.Exec(ctx, deferTaskSQL, taskID, delay.String(), reason)
	return err
}
