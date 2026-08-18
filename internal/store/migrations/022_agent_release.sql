-- Agent versions, and promoting one to production.
--
-- A definition's version counter went up on every save and nothing kept the
-- definition it counted. There was no way to see what changed, no way to go back
-- to the version that worked, and nothing stopped an edit made at 18:00 from
-- being what the 02:00 scheduled run executed — evaluated or not.

-- One row per saved version: what the definition looked like, who saved it, and
-- why. The runtime resolves the live definition as before; this is the record
-- that makes a rollback and a promotion gate possible.
CREATE TABLE IF NOT EXISTS agent_versions (
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  version integer NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  runtime_profile_id text,
  runtime_image_id text,
  security_profile_id text,
  network_profile_id text,
  mcp_bundle_id text,
  model_endpoint_id text,
  workspace_id text,
  system_prompt text NOT NULL DEFAULT '',
  spec jsonb NOT NULL DEFAULT '{}'::jsonb,
  note text NOT NULL DEFAULT '',
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent_id, version)
);

-- Which version is approved for production, and whether this agent insists on
-- that approval before the execution plane will run it.
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS promoted_version integer;
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS promoted_at timestamptz;
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS promoted_by text REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS promotion_note text NOT NULL DEFAULT '';
-- Off by default: an agent that never asked for a gate keeps running exactly as
-- it did.
ALTER TABLE agent_definitions ADD COLUMN IF NOT EXISTS require_promotion boolean NOT NULL DEFAULT false;

-- An evaluation result belongs to the definition it evaluated. Without the
-- version a passing result would keep vouching for an agent that has since been
-- rewritten.
ALTER TABLE agent_evaluations ADD COLUMN IF NOT EXISTS agent_version integer NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS agent_evaluations_version_idx ON agent_evaluations(agent_id, agent_version, status);
