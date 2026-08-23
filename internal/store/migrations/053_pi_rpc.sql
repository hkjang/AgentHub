-- Pi joins the runtime adapters, and a protocol joins the backends.
--
-- The backend is named for the shape rather than the agent: JSON lines on stdin
-- and stdout, a command acknowledged and then a stream of events, is what several
-- agents offer, and a backend named for one of them would have to be copied for
-- the next.
--
-- Keep these lists, internal/runtimetype.Supported and the AgentRuntime CRD enum
-- in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'orca', 'pi', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'opencodereview', 'orca', 'pi', 'custom'));

ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp', 'investigate', 'review', 'orca', 'rpc'));

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp', 'investigate', 'review', 'orca', 'rpc'));
