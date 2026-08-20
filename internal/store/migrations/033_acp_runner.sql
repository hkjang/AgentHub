-- Speaking a protocol instead of parsing a program's output.
--
-- The CLI runner works by knowing one agent's flags and one agent's JSON. That
-- knowledge is not transferable: the next terminal agent names its budget flags
-- differently, reports usage differently, and says "I stopped because" in its own
-- words. The Agent Client Protocol is the industry's answer to exactly that — a
-- JSON-RPC conversation over the agent's stdio, with a session, streamed updates,
-- and a permission request the client answers.
--
-- So a run driven this way is a conversation the platform can record turn by
-- turn: the agent's message, every tool call it made, and every permission the
-- platform granted or refused on the operator's behalf.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp'));

-- A tool call the agent made, and a permission the platform answered, are both
-- things that happened in the run and both have to be on its timeline.
ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp'));
