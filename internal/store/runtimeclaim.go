package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Taking runtime capacity under a quota, the same way storage is taken.
//
// The limit was read and the runtime written with nothing holding the gap, so
// two starts arriving together each saw room for the last one. It is the same
// shape as the workspace race measured in the release before this — where six
// writers released together all took the last of a quota that allowed one.

// ClaimRuntimeCapacity runs fn with this owner's runtime quota held.
//
// exceptRuntimeID leaves one runtime out of what is counted as held: the
// autonomous path creates the record before it knows the profile, so by the time
// the limit is asked the row it is about to start is already counted, and a
// person allowed one runtime was refused because of the runtime they were asking
// about.
func (s *Store) ClaimRuntimeCapacity(ctx context.Context, ownerID, profileID, exceptRuntimeID string, fn func(tx pgx.Tx) error) error {
	// The limits and the profile are read before the transaction opens. They are
	// configuration rather than something that races, and reading them from the
	// pool while holding a transaction takes a second connection — which deadlocks
	// as soon as every connection is inside one of these. That is measured, not
	// theoretical: the storage version hung until it was moved out.
	resolved, err := s.ResolveQuota(ctx, ownerID)
	if err != nil {
		return err
	}
	var addCPU, addMemory int
	if profileID != "" {
		if err := s.pool.QueryRow(ctx, `SELECT cpu_millis,memory_mb FROM runtime_profiles WHERE id=$1 AND enabled`, profileID).
			Scan(&addCPU, &addMemory); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Department first, then user: one order everywhere, so two requests needing
	// both locks cannot each hold the one the other wants.
	var departmentID *string
	if err := tx.QueryRow(ctx, `SELECT department_id FROM users WHERE id = $1`, ownerID).Scan(&departmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if departmentID != nil && *departmentID != "" {
		var ignored string
		if err := tx.QueryRow(ctx, `SELECT id FROM departments WHERE id = $1 FOR UPDATE`, *departmentID).Scan(&ignored); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, ownerID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	held, err := heldRuntimes(ctx, tx, `r.owner_id = $1`, ownerID, exceptRuntimeID)
	if err != nil {
		return err
	}
	if err := quota.CheckHeld(quota.ScopeUser, resolved.Effective, held, addCPU, addMemory); err != nil {
		return err
	}
	if departmentID != nil && *departmentID != "" {
		departmentHeld, err := heldRuntimes(ctx, tx, `u.department_id = $1`, *departmentID, exceptRuntimeID)
		if err != nil {
			return err
		}
		if err := quota.CheckHeld(quota.ScopeDepartment, resolved.DepartmentQ.Total, departmentHeld, addCPU, addMemory); err != nil {
			return err
		}
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("런타임을 저장하지 못했습니다: %w", err)
	}
	return nil
}

// heldRuntimes counts what is running, inside the caller's transaction. It is
// the same shape of count the quota screen shows, asked here under the lock so
// that what it says is still true when the row is written.
func heldRuntimes(ctx context.Context, tx pgx.Tx, where, arg, except string) (quota.Held, error) {
	var held quota.Held
	err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(p.cpu_millis),0), COALESCE(sum(p.memory_mb),0)
		FROM agent_runtimes r
		JOIN agent_definitions a ON a.id = r.agent_id
		LEFT JOIN runtime_profiles p ON p.id = a.runtime_profile_id
		LEFT JOIN users u ON u.id = r.owner_id
		WHERE `+where+` AND r.desired_state = 'running' AND ($2 = '' OR r.id <> $2)`, arg, except).
		Scan(&held.Runtimes, &held.CPUMillis, &held.MemoryMB)
	return held, err
}
