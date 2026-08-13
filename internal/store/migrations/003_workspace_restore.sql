ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS source_snapshot_id text REFERENCES workspace_snapshots(id) ON DELETE SET NULL;
