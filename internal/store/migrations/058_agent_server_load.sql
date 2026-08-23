-- Which server a run's work went to.
--
-- Registered capacity meant nothing until now: an operator could say a machine
-- holds two conversations at once and the platform would send it ten. Counting
-- needs a record of where each run went, and this is it — on the run rather than
-- on the server, so the count is derived from what is actually in flight and
-- cannot drift the way a counter kept on the server row would.
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS agent_server_id text
  REFERENCES agent_servers(id) ON DELETE SET NULL;

-- The count asks for one server's unfinished runs, which is what this index is
-- for; placement runs on every task and must not read the whole table.
CREATE INDEX IF NOT EXISTS agent_runs_server_idx ON agent_runs(agent_server_id)
  WHERE agent_server_id IS NOT NULL AND finished_at IS NULL;
