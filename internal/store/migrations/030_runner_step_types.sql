-- A run's timeline has to be able to hold what the runners actually do.
--
-- The step types were fixed when every task was a prose loop: plan, reasoning,
-- tool, artifact, completion, delegation. The flow runner records a `flow` step
-- and the CLI runner a `cli` one, and both were refused by this constraint —
-- silently, because a step that cannot be written is logged and the run carries
-- on. The result was the worst kind of gap: the two runners that do real work
-- were the two whose evidence never reached the run record, and only a task on a
-- real cluster showed it.
ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli'));
