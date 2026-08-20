-- Langflow joins the runtime adapters. It is a fourth kind of agent runtime
-- rather than a variation on the others: the work is drawn as a flow in a
-- browser and the platform can execute that saved flow directly, so the CHECK
-- constraints that pin runtime_type have to admit it before an Agent can be
-- created with it.
--
-- Keep the value lists here, internal/runtimetype.Supported and the AgentRuntime
-- CRD enum in step; a value accepted by one and rejected by another surfaces as
-- an Agent that saves and then never starts.

ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'langflow', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'langflow', 'custom'));
