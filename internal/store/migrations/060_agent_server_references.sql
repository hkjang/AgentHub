-- Deleting a registered server, done the way this platform does it everywhere
-- else.
--
-- Two references were declared backwards when the backend was added, and both
-- were wrong in the same direction: they made the delete succeed and something
-- else quietly change.
--
-- A Goal that names one machine now refuses the delete, like every other
-- platform resource an Agent is pointing at. It used to be set to null, which
-- turned a pinned Goal into an unpinned one — and an unpinned Goal takes any
-- server in any zone. The whole reason a Goal may name a machine is that the
-- reason for naming it usually cannot be written in a field, so sending its work
-- somewhere else on an administrator's unrelated cleanup is exactly the outcome
-- that must not happen quietly.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_agent_server_id_fkey;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_agent_server_id_fkey
  FOREIGN KEY (agent_server_id) REFERENCES agent_servers(id);

-- A finished run keeps the id of the machine it ran on, with no reference at
-- all. History is a record of what happened rather than a pointer at something
-- that still exists — which is how every other id on this table is kept — and
-- the previous rule erased the answer to "where did this actually run" for every
-- past run the moment a server was retired.
ALTER TABLE agent_runs DROP CONSTRAINT IF EXISTS agent_runs_agent_server_id_fkey;
