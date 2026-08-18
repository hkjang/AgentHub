-- Execution quotas.
--
-- Runtimes, CPU, memory and storage were already bounded per user; what an agent
-- could spend was not. A loop that never converges costs tokens for as long as
-- nobody opens the usage report, and one person could hold every worker slot.

-- A budget for one agent, in tokens over the reporting window. Zero means the
-- agent is bounded only by its owner's budget.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS token_budget bigint NOT NULL DEFAULT 0
  CHECK (token_budget >= 0);

-- The quota check reads one owner's spend over a window, which is the same shape
-- the usage report reads.
CREATE INDEX IF NOT EXISTS agent_run_steps_created_at_idx ON agent_run_steps(created_at);
CREATE INDEX IF NOT EXISTS agent_runs_owner_idx ON agent_runs(owner_id, started_at DESC);
