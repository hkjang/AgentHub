-- Where a task came from, when it came from outside.
--
-- An external agent can now give one of this platform's agents a job over MCP,
-- and that is worth recording as its own source rather than as a manual run:
-- "somebody clicked" and "another agent asked" answer different questions when
-- an operator is working out why a queue is full.
ALTER TABLE agent_tasks DROP CONSTRAINT IF EXISTS agent_tasks_source_check;
ALTER TABLE agent_tasks
  ADD CONSTRAINT agent_tasks_source_check CHECK (source IN ('manual', 'cron', 'webhook', 'agent', 'event', 'mcp'));
