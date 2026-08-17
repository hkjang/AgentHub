-- Agent Execution Plane.
--
-- Agents could only be driven interactively: a user opened the Runtime and typed.
-- These tables add the autonomous half — a Goal the agent works toward, Triggers
-- that start work on their own, Tasks queued for execution, and Runs recording
-- what actually happened. Nothing here replaces the interactive path; an agent
-- left in 'interactive' mode behaves exactly as before.

-- How an agent may be driven. 'interactive' is the default so every existing
-- definition keeps its current behaviour.
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS execution_mode text NOT NULL DEFAULT 'interactive'
  CHECK (execution_mode IN ('interactive', 'task', 'scheduled', 'event', 'service', 'hybrid'));

-- What the agent is trying to achieve, and how the platform decides it is done.
-- One row per agent; kept separate from the definition so a goal can be revised
-- without bumping the definition version that Runtimes are pinned to.
CREATE TABLE IF NOT EXISTS agent_goals (
  agent_id text PRIMARY KEY REFERENCES agent_definitions(id) ON DELETE CASCADE,
  description text NOT NULL DEFAULT '',
  success_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
  failure_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
  constraints text NOT NULL DEFAULT '',
  -- Execution limits. Enforced by the worker on every run, not just validated.
  max_steps integer NOT NULL DEFAULT 10 CHECK (max_steps BETWEEN 1 AND 100),
  max_tool_calls integer NOT NULL DEFAULT 50 CHECK (max_tool_calls BETWEEN 1 AND 1000),
  max_duration_seconds integer NOT NULL DEFAULT 1800 CHECK (max_duration_seconds BETWEEN 30 AND 86400),
  max_retries integer NOT NULL DEFAULT 2 CHECK (max_retries BETWEEN 0 AND 10),
  -- Whether the platform acquires a Runtime for the task and what it does after.
  start_on_demand boolean NOT NULL DEFAULT true,
  stop_after_task boolean NOT NULL DEFAULT false,
  -- How completion is decided: agent self-report, rule match, LLM judge, or a
  -- combination. 'rule' alone never trusts the agent's own claim.
  completion_strategy text NOT NULL DEFAULT 'agent'
    CHECK (completion_strategy IN ('agent', 'rule', 'judge', 'composite')),
  -- What to do when the same agent already has a run in flight.
  concurrency_policy text NOT NULL DEFAULT 'queue'
    CHECK (concurrency_policy IN ('reject', 'queue', 'parallel', 'replace')),
  max_concurrent_runs integer NOT NULL DEFAULT 1 CHECK (max_concurrent_runs BETWEEN 1 AND 20),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- What starts work without a person asking.
CREATE TABLE IF NOT EXISTS agent_triggers (
  id text PRIMARY KEY,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('manual', 'cron', 'webhook')),
  enabled boolean NOT NULL DEFAULT true,
  -- Cron only. Stored with its timezone so a schedule means the same thing
  -- regardless of where the worker runs.
  schedule text NOT NULL DEFAULT '',
  timezone text NOT NULL DEFAULT 'Asia/Seoul',
  -- Webhook only; a hashed shared secret used to verify the signature.
  webhook_secret text NOT NULL DEFAULT '',
  -- Task template applied on fire.
  task_title text NOT NULL DEFAULT '',
  task_input text NOT NULL DEFAULT '',
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('critical', 'high', 'normal', 'low', 'background')),
  last_fired_at timestamptz,
  next_fire_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(agent_id, name)
);
CREATE INDEX IF NOT EXISTS agent_triggers_due_idx ON agent_triggers(next_fire_at) WHERE enabled AND type = 'cron';

-- The unit of work. Queued here and claimed by a worker.
CREATE TABLE IF NOT EXISTS agent_tasks (
  id text PRIMARY KEY,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title text NOT NULL,
  input text NOT NULL DEFAULT '',
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('critical', 'high', 'normal', 'low', 'background')),
  status text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'planning', 'ready', 'running', 'waiting_tool', 'waiting_approval',
                      'retrying', 'completed', 'failed', 'cancelled', 'dead_letter')),
  -- Where the task came from: a person, a trigger, or another agent.
  source text NOT NULL DEFAULT 'manual',
  trigger_id text REFERENCES agent_triggers(id) ON DELETE SET NULL,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  attempts integer NOT NULL DEFAULT 0,
  -- When the task becomes eligible to run. Retry backoff pushes this forward.
  scheduled_at timestamptz NOT NULL DEFAULT now(),
  deadline_at timestamptz,
  -- Held by the worker that claimed it, so a crashed worker's task can be
  -- reclaimed once the lease expires.
  claimed_by text NOT NULL DEFAULT '',
  claimed_until timestamptz,
  current_run_id text,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- The claim query orders by priority then age among tasks that are due.
CREATE INDEX IF NOT EXISTS agent_tasks_claim_idx ON agent_tasks(status, scheduled_at) WHERE status IN ('queued', 'retrying');
CREATE INDEX IF NOT EXISTS agent_tasks_owner_idx ON agent_tasks(owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS agent_tasks_agent_idx ON agent_tasks(agent_id, created_at DESC);

-- One attempt at a task. A task that is retried has several runs, which is what
-- makes a failure and its eventual success both auditable.
CREATE TABLE IF NOT EXISTS agent_runs (
  id text PRIMARY KEY,
  task_id text NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  attempt integer NOT NULL DEFAULT 1,
  status text NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
  -- The definition version this attempt ran, so a later edit does not rewrite
  -- history.
  agent_version integer NOT NULL DEFAULT 1,
  runtime_id text,
  model_endpoint_id text,
  model_name text NOT NULL DEFAULT '',
  trace_id text NOT NULL DEFAULT '',
  worker_id text NOT NULL DEFAULT '',
  step_count integer NOT NULL DEFAULT 0,
  tool_calls integer NOT NULL DEFAULT 0,
  total_tokens integer NOT NULL DEFAULT 0,
  duration_ms bigint NOT NULL DEFAULT 0,
  result text NOT NULL DEFAULT '',
  failure_reason text NOT NULL DEFAULT '',
  -- How completion was decided and what the evaluator saw.
  completion jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_runs_task_idx ON agent_runs(task_id, attempt);
CREATE INDEX IF NOT EXISTS agent_runs_owner_idx ON agent_runs(owner_id, created_at DESC);

-- What the agent did, one reasoning turn at a time.
CREATE TABLE IF NOT EXISTS agent_run_steps (
  id text PRIMARY KEY,
  run_id text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  sequence integer NOT NULL,
  type text NOT NULL DEFAULT 'reasoning'
    CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion')),
  title text NOT NULL DEFAULT '',
  input text NOT NULL DEFAULT '',
  output text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'succeeded' CHECK (status IN ('succeeded', 'failed', 'skipped')),
  error text NOT NULL DEFAULT '',
  prompt_tokens integer NOT NULL DEFAULT 0,
  completion_tokens integer NOT NULL DEFAULT 0,
  duration_ms bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(run_id, sequence)
);

-- The timeline. Every state change is an event so a run can be replayed exactly
-- as it happened, including the parts that produced no output.
CREATE TABLE IF NOT EXISTS agent_run_events (
  id bigserial PRIMARY KEY,
  run_id text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
  type text NOT NULL,
  message text NOT NULL DEFAULT '',
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_run_events_run_idx ON agent_run_events(run_id, occurred_at);

-- What the run produced. Metadata lives here; large content is stored by the
-- configured artifact store and referenced by storage_ref.
CREATE TABLE IF NOT EXISTS agent_artifacts (
  id text PRIMARY KEY,
  run_id text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  type text NOT NULL DEFAULT 'report'
    CHECK (type IN ('report', 'file', 'patch', 'commit', 'pull_request', 'sql', 'dataset', 'image', 'json', 'log')),
  content_type text NOT NULL DEFAULT 'text/markdown',
  size_bytes bigint NOT NULL DEFAULT 0,
  -- Inline content for small text artifacts; anything larger is written to the
  -- artifact store and only referenced here.
  content text NOT NULL DEFAULT '',
  storage_ref text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_artifacts_owner_idx ON agent_artifacts(owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS agent_artifacts_run_idx ON agent_artifacts(run_id);
