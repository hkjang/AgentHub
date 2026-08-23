-- Orca joins the runtime adapters, and the execution fabric joins the backends.
--
-- It is a backend rather than only a runtime because a runtime is one Pod
-- running one agent, and that shape cannot express what Orca is for: one task
-- fanned out to several coding agents, each in its own git worktree, compared
-- afterwards. AgentHub keeps policy, quota, content inspection, audit, the model
-- gateway and the final verdict; the fabric owns coordination inside one task.
--
-- Keep these lists, internal/runtimetype.Supported and the AgentRuntime CRD enum
-- in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'orca', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'orca', 'custom'));

ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp', 'investigate', 'review', 'orca'));

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp', 'investigate', 'review', 'orca'));

-- What the fabric did, kept beside the run so a person can see which worker
-- worked in which checkout without opening the fabric's own screens. The ids are
-- Orca's, stored verbatim: they are what `orca orchestration dispatch-show`
-- takes, so a record here can be checked against the fabric that produced it.
CREATE TABLE IF NOT EXISTS orca_dispatches (
  id           text PRIMARY KEY,
  run_id       text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  orca_run_id  text NOT NULL DEFAULT '',
  orca_task_id text NOT NULL DEFAULT '',
  terminal     text NOT NULL DEFAULT '',
  worktree     text NOT NULL DEFAULT '',
  branch       text NOT NULL DEFAULT '',
  role         text NOT NULL DEFAULT '',
  status       text NOT NULL DEFAULT 'dispatched',
  detail       text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS orca_dispatches_run_idx ON orca_dispatches (run_id, created_at);
