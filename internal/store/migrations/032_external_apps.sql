-- Applications the platform drives but does not run.
--
-- Dify is the case this exists for. It is not one container but a dozen — api,
-- worker, beat, web, plugin daemon, sandboxes, a proxy, PostgreSQL, Redis and a
-- vector store — and reproducing that topology inside a Pod would mean carrying a
-- fork of somebody else's deployment and following it every release. What a site
-- actually has is a Dify they already run; what they want from AgentHub is for a
-- task to call one of its apps and for the result to land in a run record with
-- the policy, content scanning, quota and audit trail around it.
--
-- So the platform stores the address and the credential, and calls it. The same
-- table will hold anything else with that shape.
CREATE TABLE IF NOT EXISTS external_apps (
  id text PRIMARY KEY,
  name text NOT NULL,
  -- Which product's API this speaks. One provider today; the column exists
  -- because the second one will not want its own table.
  provider text NOT NULL DEFAULT 'dify' CHECK (provider IN ('dify')),
  base_url text NOT NULL,
  -- Dify's two service APIs answer differently: a workflow returns outputs, a chat
  -- app returns an answer. The caller has to know which before it asks.
  app_kind text NOT NULL DEFAULT 'workflow' CHECK (app_kind IN ('workflow', 'chat')),
  -- The API key belongs to one app inside that deployment, which is why the
  -- credential and the app are one row rather than two.
  secret_value text,
  description text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- A run's timeline has to be able to hold the call, too.
ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external'));

-- Where a Dify-backed Goal sends its work.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify'));

ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS external_app_id text REFERENCES external_apps(id) ON DELETE SET NULL;
-- A workflow app takes named inputs rather than a prompt, so the platform has to
-- be told which variable the task's text belongs in. Empty means the product's
-- own default for that kind.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS external_input_key text NOT NULL DEFAULT '';
