CREATE TABLE IF NOT EXISTS agent_workflows (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  mode text NOT NULL CHECK (mode IN ('sequential','parallel','router','supervisor','reviewer','consensus')),
  max_depth integer NOT NULL DEFAULT 4 CHECK (max_depth BETWEEN 1 AND 20),
  max_agent_calls integer NOT NULL DEFAULT 12 CHECK (max_agent_calls BETWEEN 1 AND 100),
  max_tool_calls integer NOT NULL DEFAULT 50 CHECK (max_tool_calls BETWEEN 1 AND 1000),
  max_duration_seconds integer NOT NULL DEFAULT 900 CHECK (max_duration_seconds BETWEEN 10 AND 86400),
  max_parallel_agents integer NOT NULL DEFAULT 3 CHECK (max_parallel_agents BETWEEN 1 AND 20),
  definition jsonb NOT NULL DEFAULT '{"steps":[]}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, name)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
  id text PRIMARY KEY,
  workflow_id text NOT NULL REFERENCES agent_workflows(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'validating',
  input jsonb NOT NULL DEFAULT '{}'::jsonb,
  output jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workflow_runs_owner_idx ON workflow_runs(owner_id, created_at DESC);
