package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrInUse means the row is still referenced by another record. Platform
// resources are shared, so deleting one out from under a live Agent definition
// would leave it unschedulable; callers surface this as a 409 instead of a 500.
var ErrInUse = errors.New("resource is still referenced")

// foreignKeyViolation is the PostgreSQL SQLSTATE for a referencing row.
const foreignKeyViolation = "23503"

func translateDeleteError(err error, label string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return fmt.Errorf("%w: %s", ErrInUse, label)
	}
	return err
}

// deleteScoped removes one row, optionally restricted to an owner. It reports
// ErrNotFound when nothing matched so a caller cannot silently "delete" another
// user's record, and ErrInUse when a foreign key still points at it.
func (s *Store) deleteScoped(ctx context.Context, table, id, ownerID string, admin bool, label string) error {
	query := `DELETE FROM ` + table + ` WHERE id=$1`
	args := []any{id}
	if !admin {
		query += ` AND owner_id=$2`
		args = append(args, ownerID)
	}
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return translateDeleteError(err, label)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteWorkflow(ctx context.Context, id, ownerID string) error {
	return s.deleteScoped(ctx, "agent_workflows", id, ownerID, false, "workflow")
}

func (s *Store) DeleteEvaluationTestSet(ctx context.Context, id, ownerID string) error {
	return s.deleteScoped(ctx, "evaluation_test_sets", id, ownerID, false, "evaluation test set")
}

// DeleteWorkspaceSnapshot removes the snapshot record. The caller deletes the
// CSI VolumeSnapshot separately; the storage reference is returned so it can.
func (s *Store) DeleteWorkspaceSnapshot(ctx context.Context, id, ownerID string) (WorkspaceSnapshot, error) {
	item, _, err := s.WorkspaceSnapshotByID(ctx, ownerID, id)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_snapshots ws USING workspaces w WHERE ws.id=$1 AND ws.workspace_id=w.id AND w.owner_id=$2`, id, ownerID)
	if err != nil {
		return WorkspaceSnapshot{}, translateDeleteError(err, "workspace snapshot")
	}
	if tag.RowsAffected() == 0 {
		return WorkspaceSnapshot{}, ErrNotFound
	}
	return item, nil
}

// UpdateWorkspace renames a workspace. Type, size and the bound PVC are fixed
// once provisioned: changing them would silently detach the volume that already
// holds the user's files.
func (s *Store) UpdateWorkspace(ctx context.Context, id, ownerID, name string) (Workspace, error) {
	var item Workspace
	err := s.pool.QueryRow(ctx, `UPDATE workspaces SET name=$3,updated_at=now() WHERE id=$1 AND owner_id=$2 RETURNING id,owner_id,name,type,size_gb,repository_url,branch,pvc_name,source_snapshot_id,status,created_at,updated_at`, id, ownerID, name).
		Scan(&item.ID, &item.OwnerID, &item.Name, &item.Type, &item.SizeGB, &item.RepositoryURL, &item.Branch, &item.PVCName, &item.SourceSnapshotID, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	if err != nil {
		return Workspace{}, err
	}
	return item, nil
}

// WorkspaceAgentRefs lists the agents still bound to a workspace. Deleting a
// workspace out from under them would leave the definitions unschedulable, so
// the API reports these names instead of failing on the foreign key.
func (s *Store) WorkspaceAgentRefs(ctx context.Context, id, ownerID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM agent_definitions WHERE workspace_id=$1 AND owner_id=$2 ORDER BY name`, id, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// DeleteWorkspace removes the record. workspace_snapshots cascade; the caller is
// responsible for the PVC, which is deliberately preserved so that a mistaken
// delete does not destroy the user's files.
func (s *Store) DeleteWorkspace(ctx context.Context, id, ownerID string) (Workspace, error) {
	item, err := s.WorkspaceByID(ctx, id, ownerID, false)
	if err != nil {
		return Workspace{}, err
	}
	if err := s.deleteScoped(ctx, "workspaces", id, ownerID, false, "workspace"); err != nil {
		return Workspace{}, err
	}
	return item, nil
}

// adminDeletableTables maps the admin resource kinds exposed by the API to their
// tables. Restricting deletes to this set keeps the table name out of user input.
var adminDeletableTables = map[string]string{
	"runtime-profiles": "runtime_profiles",
	"runtime-images":   "runtime_images",
	"models":           "model_endpoints",
	"mcp-servers":      "mcp_servers",
	"mcp-bundles":      "mcp_bundles",
}

// adminResourceTable resolves an API resource kind to its table. Policy profiles
// live in their own per-kind tables, so they go through the same helper the read
// and upsert paths use rather than a second copy of the mapping.
func adminResourceTable(kind string) (string, error) {
	if table, ok := adminDeletableTables[kind]; ok {
		return table, nil
	}
	policyKind, _, found := strings.Cut(kind, "-profiles")
	if found {
		if table, err := policyTable(policyKind); err == nil {
			return table, nil
		}
	}
	return "", fmt.Errorf("unknown admin resource %q", kind)
}

// DeleteAdminResource removes a shared platform resource.
func (s *Store) DeleteAdminResource(ctx context.Context, kind, id string) error {
	table, err := adminResourceTable(kind)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM `+table+` WHERE id=$1`, id)
	if err != nil {
		return translateDeleteError(err, kind)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MCPServerBundleRefs lists bundles referencing an MCP server. The membership is
// a text[] column rather than a foreign key, so the database cannot block the
// delete on its own.
func (s *Store) MCPServerBundleRefs(ctx context.Context, serverID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM mcp_bundles WHERE $1 = ANY(server_ids) ORDER BY name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
