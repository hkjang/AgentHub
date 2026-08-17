-- Phase C: autonomous control.
--
-- The execution plane could run a task to completion, but it could not plan
-- first, pause for a human before a state-changing action, remember anything
-- between runs, or hand work to another agent. These tables add those four.

-- How much planning the platform imposes. OpenCode and Hermes plan for
-- themselves, so forcing a platform plan on them would fight their own agent
-- loop; 'native' leaves them to it and is the default.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS planner_mode text NOT NULL DEFAULT 'native'
  CHECK (planner_mode IN ('none', 'native', 'platform', 'hybrid'));

-- When true the agent must obtain approval before any action it declares as
-- state-changing. The task parks in waiting_approval rather than proceeding.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS approval_required boolean NOT NULL DEFAULT false;

-- How deep a chain of agent-to-agent delegation may go. 0 forbids delegation.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS max_delegation_depth integer NOT NULL DEFAULT 0
  CHECK (max_delegation_depth BETWEEN 0 AND 5);

-- Delegation lineage. A task created by another agent records its parent so the
-- chain can be bounded and a cycle detected before it runs.
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS parent_task_id text REFERENCES agent_tasks(id) ON DELETE SET NULL;
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS delegation_depth integer NOT NULL DEFAULT 0;

-- The approval a parked task is waiting on.
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS approval_id text REFERENCES approvals(id) ON DELETE SET NULL;

-- The plan a run worked from, kept so a completed run can be read back as
-- "this is what it intended to do" alongside "this is what it did".
CREATE TABLE IF NOT EXISTS agent_plans (
  id text PRIMARY KEY,
  run_id text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
  mode text NOT NULL DEFAULT 'platform',
  goal text NOT NULL DEFAULT '',
  steps jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_plans_run_idx ON agent_plans(run_id);

-- What the agent remembers between runs.
--
-- This is deliberately separate from the Runtime home volume: the home holds
-- the adapter's own configuration and is tied to a Pod, whereas memory belongs
-- to the agent and must outlive any Runtime. Scope decides how long an entry
-- lives and who can see it.
CREATE TABLE IF NOT EXISTS agent_memories (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- 'agent' persists for the agent, 'task' only within one task's runs,
  -- 'workspace' is shared by every agent bound to that workspace.
  scope text NOT NULL DEFAULT 'agent' CHECK (scope IN ('task', 'agent', 'workspace')),
  agent_id text REFERENCES agent_definitions(id) ON DELETE CASCADE,
  task_id text REFERENCES agent_tasks(id) ON DELETE CASCADE,
  workspace_id text REFERENCES workspaces(id) ON DELETE CASCADE,
  key text NOT NULL,
  value text NOT NULL DEFAULT '',
  -- Which run last wrote this, so a surprising memory can be traced back.
  written_by_run_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- One entry per key within its scope; a rewrite replaces rather than accumulates.
CREATE UNIQUE INDEX IF NOT EXISTS agent_memories_agent_key_idx ON agent_memories(agent_id, key) WHERE scope = 'agent';
CREATE UNIQUE INDEX IF NOT EXISTS agent_memories_task_key_idx ON agent_memories(task_id, key) WHERE scope = 'task';
CREATE UNIQUE INDEX IF NOT EXISTS agent_memories_workspace_key_idx ON agent_memories(workspace_id, key) WHERE scope = 'workspace';
