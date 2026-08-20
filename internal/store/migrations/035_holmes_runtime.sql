-- HolmesGPT joins the runtime adapters, and investigation joins the backends.
--
-- The other backends answer a question. This one answers it and hands back what
-- it looked at: every query it ran against metrics, logs and alerts, with the
-- result of each. An investigation whose evidence cannot be checked is an
-- opinion, so the evidence goes on the run's timeline beside the conclusion —
-- which is what makes this a backend of its own rather than a prompt.
--
-- Keep these value lists, internal/runtimetype.Supported and the AgentRuntime CRD
-- enum in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));

ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli', 'dify', 'acp', 'investigate'));

ALTER TABLE agent_run_steps DROP CONSTRAINT IF EXISTS agent_run_steps_type_check;
ALTER TABLE agent_run_steps ADD CONSTRAINT agent_run_steps_type_check
  CHECK (type IN ('plan', 'reasoning', 'tool', 'artifact', 'completion', 'delegation', 'flow', 'cli', 'external', 'acp', 'investigate'));
