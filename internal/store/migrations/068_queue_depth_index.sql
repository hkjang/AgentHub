-- Every worker reads the queue depth on every scale tick, and reading it meant a
-- full scan of agent_tasks: the query counts what is ready and what is running,
-- and had no condition the planner could narrow, so it walked every task the
-- retention window keeps. Finished tasks are almost all of them.
--
-- Measured on 500,000 tasks holding about a thousand active rows:
--   one pass, two filters   33.7  32.2  33.5 ms   parallel scan, 3 workers
--   ready, on the claim index      4.9  0.57  0.50 ms
--   running, on this index         4.0  0.38  0.34 ms   index-only scan
--
-- The ready half already had an index — the one the claim itself uses. This is
-- the other half: the statuses that mean a worker is holding the task now.
CREATE INDEX IF NOT EXISTS agent_tasks_active_idx
  ON agent_tasks(status) WHERE status IN ('planning', 'ready', 'running', 'waiting_tool');
