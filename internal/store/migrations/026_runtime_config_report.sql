-- What a runtime actually started with.
--
-- The platform generates each runtime's configuration and an administrator can now
-- overlay their own settings onto it, but nothing came back from inside the Pod.
-- A setting that silently did not apply is worse than one that was never offered:
-- the operator believes the fleet is configured, and the only way to find out was
-- to open a runtime and read the file by hand.
CREATE TABLE IF NOT EXISTS runtime_config_reports (
  runtime_id text PRIMARY KEY REFERENCES agent_runtimes(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  runtime_type text NOT NULL DEFAULT '',
  -- fingerprint is the settings version the Pod applied. Comparing it with what the
  -- platform would send now is what turns "I saved it" into "the fleet runs it".
  fingerprint text NOT NULL DEFAULT '',
  -- status is applied, missing or unreadable, as judged inside the Pod by reading
  -- back the file it had just written.
  status text NOT NULL DEFAULT 'applied',
  detail text NOT NULL DEFAULT '',
  -- file is which configuration was read back, and keys are what is in it. Keys
  -- only: an overlay may carry an internal endpoint or a licence string, and
  -- neither belongs in a status record.
  file text NOT NULL DEFAULT '',
  keys jsonb NOT NULL DEFAULT '[]'::jsonb,
  reported_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS runtime_config_reports_agent_idx ON runtime_config_reports(agent_id, reported_at DESC);
