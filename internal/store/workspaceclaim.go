package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hkjang/AgentHub/internal/quota"
)

// Taking storage under a quota has to be one act.
//
// The quota was read and the workspace written with nothing holding the gap, so
// two requests arriving together each saw room for the last hundred gigabytes
// and each took it. It is the same shape as the placement race this platform
// already fixed and measured — there, eight writers released together produced
// two winners where one was allowed.
//
// So the owner's row is locked first. Two workspaces being created for the same
// person queue behind each other for the microsecond it takes, and the second
// then counts a total that includes the first. Different owners do not meet at
// all, unless they share a department, whose row is locked too.

// ClaimWorkspaceStorage runs fn with this owner's storage quota held.
//
// The callback does the writing. Everything inside it — the count, the limit,
// the insert — happens under the same lock, which is what the previous version
// could not say.
func (s *Store) ClaimWorkspaceStorage(ctx context.Context, ownerID string, addGB int, fn func(tx pgx.Tx) error) error {
	// The limits are read before the transaction opens, on purpose. They are
	// configuration rather than something that races, and reading them from the
	// pool while holding a transaction takes a second connection — so once every
	// connection in the pool was inside one of these, none of them could finish.
	// That is not a theory: this hung until it was moved out.
	resolved, err := s.ResolveQuota(ctx, ownerID)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The department first, then the user: one order everywhere, so two requests
	// that need both locks cannot each hold the one the other wants.
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

	// Counted inside the lock rather than taken from the resolved snapshot: that
	// snapshot was read before the lock, which is exactly the gap this closes.
	var held int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(size_gb), 0) FROM workspaces WHERE owner_id = $1`, ownerID).Scan(&held); err != nil {
		return err
	}
	if err := quota.CheckStorage(quota.ScopeUser, resolved.Effective, held, addGB); err != nil {
		return err
	}
	if departmentID != nil && *departmentID != "" {
		var departmentHeld int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(w.size_gb), 0) FROM workspaces w
			JOIN users u ON u.id = w.owner_id WHERE u.department_id = $1`, *departmentID).Scan(&departmentHeld); err != nil {
			return err
		}
		if err := quota.CheckStorage(quota.ScopeDepartment, resolved.DepartmentQ.Total, departmentHeld, addGB); err != nil {
			return err
		}
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("작업공간을 저장하지 못했습니다: %w", err)
	}
	return nil
}
