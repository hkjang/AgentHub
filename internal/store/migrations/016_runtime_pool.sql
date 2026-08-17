-- Runtime warm pool.
--
-- A scheduled task pays for a cold Pod: the image is local, but the volume, the
-- adapter's init containers and the agent's own startup still take most of a
-- minute, and a trigger that fires at 08:00 does not start working at 08:00.
-- Warming the runtime ahead of a known fire time moves that cost off the
-- critical path, and holding it briefly afterwards means a burst of tasks pays
-- it once rather than per task.
--
-- The pool is per agent rather than a pool of interchangeable Pods. A runtime
-- carries its agent's identity in its mounted workspace, its configuration and
-- its secret, all bound when the Pod is created, so a generic warm Pod could not
-- become this agent's runtime without being restarted — which is the cost the
-- pool exists to avoid.

-- How long before a scheduled fire the runtime should already be up, and how
-- long to hold it after a task ends. Zero keeps today's behaviour exactly.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS warmup_seconds integer NOT NULL DEFAULT 0;
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS keep_warm_seconds integer NOT NULL DEFAULT 0;

-- When the pool may stop this runtime. NULL means the pool has no claim on it,
-- which is what keeps it from touching a runtime a person is working in.
ALTER TABLE agent_runtimes ADD COLUMN IF NOT EXISTS warm_until timestamptz;

CREATE INDEX IF NOT EXISTS agent_runtimes_warm_idx ON agent_runtimes(warm_until) WHERE warm_until IS NOT NULL;
