-- Who counted the tokens on a run.
--
-- A run with zero tokens reads as free work. Sometimes it was: the platform made
-- no model calls of its own. Usually it means the agent did the work in its own
-- process and never said what it spent, which is a different fact and needs a
-- different answer — set a token budget somewhere the platform can see it, or use
-- a runtime that reports.
--
-- Empty is for the runs that finished before this was recorded. They are not
-- relabelled, because guessing at history is how a report stops being evidence.
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS metering text NOT NULL DEFAULT '';
ALTER TABLE agent_runs DROP CONSTRAINT IF EXISTS agent_runs_metering_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_metering_check
  CHECK (metering IN ('', 'gateway', 'agent', 'context_only', 'unmetered'));
