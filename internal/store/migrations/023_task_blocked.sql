-- Tasks held back by a promotion gate.
--
-- The gate failed them instead, which meant a nightly run that arrived while an
-- unpromoted edit was live had to be recreated by hand after somebody promoted —
-- and the failure looked identical to an agent that could not do its job. A
-- blocked task is not a failure: it is work that is ready to run as soon as the
-- version it needs is approved.
ALTER TABLE agent_tasks DROP CONSTRAINT IF EXISTS agent_tasks_status_check;
ALTER TABLE agent_tasks ADD CONSTRAINT agent_tasks_status_check
  CHECK (status IN ('queued', 'planning', 'ready', 'running', 'waiting_tool', 'waiting_approval',
                    'retrying', 'completed', 'failed', 'cancelled', 'dead_letter', 'blocked'));

-- Releasing them is a per-agent operation performed the moment a version is
-- promoted, so it is worth an index of its own.
CREATE INDEX IF NOT EXISTS agent_tasks_blocked_idx ON agent_tasks(agent_id) WHERE status = 'blocked';
