-- What a workflow run cost, in columns rather than inside its output document.
--
-- A workflow calls the same models a task does, and its spend was recorded only
-- inside the run's JSON output — where the usage report cannot see it and no
-- budget could ever bound it. A person over their token budget was refused at
-- the task queue and welcome in the workflow screen, which made the budget a
-- suggestion.
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS total_tokens bigint NOT NULL DEFAULT 0;
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS agent_calls integer NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS workflow_runs_spend_idx ON workflow_runs(owner_id, created_at DESC) WHERE total_tokens > 0;
