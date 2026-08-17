package store

import (
	"context"
	"time"
)

// The runtime warm pool's queries.
//
// The pool is per agent rather than a set of interchangeable Pods: a runtime
// carries its agent's workspace, configuration and secret, all bound when the
// Pod is created, so a generic warm Pod could not become this agent's runtime
// without a restart — the very cost the pool exists to avoid.

// WarmCandidate is an agent whose runtime should be up before its next
// scheduled fire.
type WarmCandidate struct {
	AgentID   string    `json:"agentId"`
	AgentName string    `json:"agentName"`
	OwnerID   string    `json:"ownerId"`
	TriggerID string    `json:"triggerId"`
	FireAt    time.Time `json:"fireAt"`
	// WarmUntil is how long the pool may hold the runtime once it is up: the
	// fire time plus the agent's keep-warm window, so a runtime started for a
	// schedule is not stopped a second before the task that needed it.
	WarmUntil time.Time `json:"warmUntil"`
}

// RuntimesToWarm lists agents with a cron trigger due inside their warm-up
// window whose runtime is not already running.
func (s *Store) RuntimesToWarm(ctx context.Context, limit int) ([]WarmCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.agent_id, a.name, t.owner_id, t.id, t.next_fire_at,
		       t.next_fire_at + make_interval(secs => GREATEST(g.keep_warm_seconds, 60))
		FROM agent_triggers t
		JOIN agent_definitions a ON a.id = t.agent_id
		JOIN agent_goals g ON g.agent_id = t.agent_id
		WHERE t.enabled AND t.type = 'cron' AND t.next_fire_at IS NOT NULL
		  AND g.warmup_seconds > 0
		  -- Inside the window, and not so far past the fire time that the
		  -- schedule has plainly stopped advancing.
		  AND t.next_fire_at <= now() + make_interval(secs => g.warmup_seconds)
		  AND t.next_fire_at > now() - interval '10 minutes'
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_runtimes r
		    WHERE r.agent_id = t.agent_id AND r.desired_state = 'running'
		  )
		ORDER BY t.next_fire_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WarmCandidate{}
	for rows.Next() {
		var item WarmCandidate
		if err := rows.Scan(&item.AgentID, &item.AgentName, &item.OwnerID, &item.TriggerID, &item.FireAt, &item.WarmUntil); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CoolCandidate is a runtime the pool started whose hold has expired.
type CoolCandidate struct {
	RuntimeID string `json:"runtimeId"`
	AgentID   string `json:"agentId"`
	OwnerID   string `json:"ownerId"`
}

// RuntimesToCool lists runtimes the pool may now stop.
//
// A runtime with work still queued or running is left alone: stopping it would
// only make the next task pay to start it again, which is the opposite of what
// the pool is for.
func (s *Store) RuntimesToCool(ctx context.Context, limit int) ([]CoolCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.agent_id, r.owner_id
		FROM agent_runtimes r
		WHERE r.warm_until IS NOT NULL AND r.warm_until < now()
		  AND r.desired_state = 'running'
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_tasks t
		    WHERE t.agent_id = r.agent_id
		      AND t.status IN ('queued', 'planning', 'ready', 'running', 'waiting_tool', 'retrying')
		  )
		ORDER BY r.warm_until
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CoolCandidate{}
	for rows.Next() {
		var item CoolCandidate
		if err := rows.Scan(&item.RuntimeID, &item.AgentID, &item.OwnerID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ClaimWarmRuntime marks a runtime as held by the pool until the given time.
//
// The update only wins when no other worker has already claimed a later hold,
// so two workers warming the same agent cannot shorten each other's window.
func (s *Store) ClaimWarmRuntime(ctx context.Context, runtimeID string, until time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_runtimes SET warm_until=$2, updated_at=now()
		WHERE id=$1 AND (warm_until IS NULL OR warm_until < $2)`, runtimeID, until)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReleaseWarmRuntime drops the pool's claim, which is what keeps it from
// stopping a runtime a person has since taken over.
func (s *Store) ReleaseWarmRuntime(ctx context.Context, runtimeID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runtimes SET warm_until=NULL, updated_at=now() WHERE id=$1`, runtimeID)
	return err
}

// WarmRuntime is one entry of the pool as the console shows it.
type WarmRuntime struct {
	RuntimeID string    `json:"runtimeId"`
	AgentID   string    `json:"agentId"`
	AgentName string    `json:"agentName"`
	Status    string    `json:"status"`
	WarmUntil time.Time `json:"warmUntil"`
}

// WarmRuntimes lists what the pool is currently holding for one owner.
func (s *Store) WarmRuntimes(ctx context.Context, ownerID string) ([]WarmRuntime, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.agent_id, a.name, r.status, r.warm_until
		FROM agent_runtimes r JOIN agent_definitions a ON a.id = r.agent_id
		WHERE r.warm_until IS NOT NULL AND ($1 = '' OR r.owner_id = $1)
		ORDER BY r.warm_until`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WarmRuntime{}
	for rows.Next() {
		var item WarmRuntime
		if err := rows.Scan(&item.RuntimeID, &item.AgentID, &item.AgentName, &item.Status, &item.WarmUntil); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
