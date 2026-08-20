-- Goose joins the runtime adapters.
--
-- It is here because it speaks the Agent Client Protocol natively — `goose acp`
-- runs it as a protocol peer on stdio — so it needed no execution code at all:
-- the descriptor names the command, and the ACP backend added in the previous
-- release does the rest. That was the point of adopting somebody else's protocol
-- rather than learning another agent's flags and exit codes.
--
-- Keep these value lists, internal/runtimetype.Supported and the AgentRuntime CRD
-- enum in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));
