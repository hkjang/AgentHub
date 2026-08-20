-- JupyterLab, Node-RED and n8n join the runtime adapters.
--
-- JupyterLab is the analyst's bench: notebooks and a terminal, with the Qwen Code
-- agent in the same image so the person and the agent share one workspace.
-- Node-RED and n8n are not agents at all — they are the wiring people reach for
-- when the task is to move something from one system to another on a schedule or
-- an event. The platform gives all three what it gives every runtime — an
-- isolated Pod, a persistent home, an authenticated way in — and for the last two
-- nothing more, because their flows run inside them rather than through the
-- execution plane.
--
-- Keep these value lists, internal/runtimetype.Supported and the AgentRuntime CRD
-- enum in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));
