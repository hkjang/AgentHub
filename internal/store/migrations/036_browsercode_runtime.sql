-- BrowserCode joins the runtime adapters.
--
-- It is the one runtime that can reach a web page. The others read files, query
-- metrics or call APIs; this one drives a real Chromium through the DevTools
-- protocol, writing the JavaScript it needs as it goes. Like Goose it speaks the
-- Agent Client Protocol natively, so it needed no execution code — the descriptor
-- names the command and the ACP backend does the rest.
--
-- Keep these value lists, internal/runtimetype.Supported and the AgentRuntime CRD
-- enum in step; a value one accepts and another refuses surfaces as an Agent that
-- saves and then never starts.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'goose', 'holmes', 'browsercode', 'jupyter', 'langflow', 'nodered', 'n8n', 'custom'));
