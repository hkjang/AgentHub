-- Step-level checkpoint retry.
--
-- A retry used to start the task over from step one: every reasoning step was
-- paid for again, and any step that had already changed something outside the
-- platform ran a second time. A retry now resumes from the work the previous
-- attempts completed, so this records what "already completed" means.

-- Only steps recorded after this moment may be resumed. A manual "start over"
-- stamps it with now(), which retires the earlier steps without deleting the
-- record of them — and lets that fresh attempt's own steps be resumed later.
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS checkpoint_after timestamptz;

-- How many steps this run inherited, so the timeline can say where it started
-- rather than presenting resumed work as its own.
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS resumed_steps integer NOT NULL DEFAULT 0;

-- Delegation results are part of what a resumed attempt must not repeat, so they
-- are recorded as steps of their own now.
ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation'));

-- The checkpoint reads a task's steps in order across its runs.
CREATE INDEX IF NOT EXISTS agent_run_steps_created_idx ON agent_run_steps(run_id, created_at);

-- Resuming is the default, because repeating completed work is both a cost and a
-- second side effect. An agent whose steps must always run from the beginning —
-- one that cannot tell what it already did — turns it off.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS resume_from_checkpoint boolean NOT NULL DEFAULT true;
