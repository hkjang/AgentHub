-- Which registered server a goal's work goes to.
--
-- Left empty on purpose in most goals: naming a server pins the work to one
-- machine, and a site with four of them wants placement to choose. The zone is
-- the middle ground — "anywhere inside the secure network" — which is what an
-- operator actually means when they care where work runs.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS agent_server_id text
  REFERENCES agent_servers(id) ON DELETE SET NULL;
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS agent_server_zone text NOT NULL DEFAULT '';
-- Where the agent works on that server. Relative to the server's own workspace
-- root; empty means the server's default.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS agent_server_dir text NOT NULL DEFAULT '';

-- The database is the last word on which backends exist. A runner the platform
-- can choose and the schema refuses would fail at the moment somebody saves a
-- goal, which is far from where the backend was added — a guard checks these
-- lists against the code for exactly that reason.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp', 'investigate', 'review', 'orca', 'rpc', 'agentserver'));

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp', 'investigate', 'review', 'orca', 'rpc', 'agentserver'));
