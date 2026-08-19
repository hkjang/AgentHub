-- Handing a task to a person in the runtime.
--
-- Autonomous execution is a prose loop against the model gateway: it cannot edit
-- a file, run a command or click anything. Everything that needed those had two
-- endings — a failed task, or a model claiming work it never did, which is the
-- one models reliably chose. A handed-off task is neither: it is work that got as
-- far as it could and is now waiting for somebody to open the same workspace.
ALTER TABLE agent_tasks DROP CONSTRAINT IF EXISTS agent_tasks_status_check;
ALTER TABLE agent_tasks ADD CONSTRAINT agent_tasks_status_check
  CHECK (status IN ('queued', 'planning', 'ready', 'running', 'waiting_tool', 'waiting_approval',
                    'retrying', 'completed', 'failed', 'cancelled', 'dead_letter', 'blocked', 'handoff'));

CREATE INDEX IF NOT EXISTS agent_tasks_handoff_idx ON agent_tasks(owner_id, updated_at DESC) WHERE status = 'handoff';
