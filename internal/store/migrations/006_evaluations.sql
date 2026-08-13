CREATE TABLE IF NOT EXISTS evaluation_test_sets (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  pass_threshold integer NOT NULL DEFAULT 100 CHECK (pass_threshold BETWEEN 1 AND 100),
  cases jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, name)
);

CREATE TABLE IF NOT EXISTS agent_evaluations (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  test_set_id text NOT NULL REFERENCES evaluation_test_sets(id) ON DELETE CASCADE,
  status text NOT NULL,
  score integer NOT NULL DEFAULT 0,
  metrics jsonb NOT NULL DEFAULT '{}'::jsonb,
  result jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS agent_evaluations_owner_idx ON agent_evaluations(owner_id, created_at DESC);
