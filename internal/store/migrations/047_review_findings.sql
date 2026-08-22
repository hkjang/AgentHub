-- Open Code Review joins the runtime adapters, and review joins the backends.
--
-- Keep these value lists, internal/runtimetype.Supported and the AgentRuntime CRD
-- enum in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'custom'));

ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp', 'investigate', 'review'));

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp', 'investigate', 'review'));

-- The review Goal's own settings. They are columns rather than a JSON blob
-- because the console filters and the quality gate compare on them, and a gate
-- that reads a blob is a gate nobody can index or check.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_mode text NOT NULL DEFAULT 'workspace';
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_base_ref text NOT NULL DEFAULT '';
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_head_ref text NOT NULL DEFAULT '';
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_path text NOT NULL DEFAULT '';
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_exclude text NOT NULL DEFAULT '';
-- The severity at which a review fails its task. Empty means a review reports
-- and never fails, which is the right default: a gate nobody chose should not
-- start blocking work the day it ships.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS review_fail_on text NOT NULL DEFAULT '';
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_review_mode_check
  CHECK (review_mode IN ('workspace', 'range', 'commit', 'scan'));

-- What a code review found, kept as findings rather than as prose.
--
-- A review runner does not end with an answer the way the other runners do. It
-- ends with a list of located, categorised, severity-ranked observations, and
-- flattening that into a paragraph throws away the part worth having: the file,
-- the line, and whether anybody has decided about it yet.
--
-- The severity and category values are the review engine's own, stored verbatim
-- rather than mapped, so a finding means the same thing in both systems.
CREATE TABLE IF NOT EXISTS review_findings (
  id            text PRIMARY KEY,
  run_id        text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  task_id       text REFERENCES agent_tasks(id) ON DELETE SET NULL,
  agent_id      text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  file_path     text NOT NULL,
  start_line    integer NOT NULL DEFAULT 0,
  end_line      integer NOT NULL DEFAULT 0,
  severity      text NOT NULL CHECK (severity IN ('critical','high','medium','low')),
  category      text NOT NULL CHECK (category IN ('bug','security','performance','maintainability','test','style','documentation','other')),
  message       text NOT NULL DEFAULT '',
  existing_code text NOT NULL DEFAULT '',
  suggestion    text NOT NULL DEFAULT '',
  -- What a person did about it. 'open' until somebody decides; 'accepted' means
  -- it is real and 'dismissed' means it is not, and the difference is what makes
  -- the next review's noise measurable.
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open','accepted','dismissed','fixed')),
  decided_by    text REFERENCES users(id) ON DELETE SET NULL,
  decided_at    timestamptz,
  source        text NOT NULL DEFAULT 'open-code-review',
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- The two ways this table is read: one review's findings, and one owner's open
-- ones across every review.
CREATE INDEX IF NOT EXISTS review_findings_run_idx ON review_findings (run_id, severity);
CREATE INDEX IF NOT EXISTS review_findings_owner_idx ON review_findings (owner_id, status, created_at DESC);

-- What the review as a whole covered, which is the claim the findings rest on.
-- Without it a run with no findings cannot be told apart from a run that failed
-- to read anything, and those mean opposite things.
CREATE TABLE IF NOT EXISTS review_runs (
  run_id          text PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
  mode            text NOT NULL DEFAULT 'workspace',
  base_ref        text NOT NULL DEFAULT '',
  head_ref        text NOT NULL DEFAULT '',
  resolved_base   text NOT NULL DEFAULT '',
  resolved_head   text NOT NULL DEFAULT '',
  files_selected  integer NOT NULL DEFAULT 0,
  files_reviewed  integer NOT NULL DEFAULT 0,
  files_failed    integer NOT NULL DEFAULT 0,
  session_id      text NOT NULL DEFAULT '',
  engine_version  text NOT NULL DEFAULT '',
  status          text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now()
);
