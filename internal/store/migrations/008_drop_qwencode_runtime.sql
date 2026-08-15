-- The `qwencode` runtime adapter was withdrawn: it never had its own launch
-- behaviour and simply re-used the OpenCode server, so it duplicated the
-- `opencode` runtime under a second name. Existing rows are remapped to
-- `opencode`, which is what they were actually running, and the CHECK
-- constraints are re-created without the withdrawn value so the database agrees
-- with internal/runtimetype.Supported and the AgentRuntime CRD enum.

UPDATE agent_definitions SET runtime_type = 'opencode' WHERE runtime_type = 'qwencode';
UPDATE agent_templates SET runtime_type = 'opencode' WHERE runtime_type = 'qwencode';
UPDATE runtime_images SET runtime_type = 'opencode' WHERE runtime_type = 'qwencode';

-- Templates are seeded by slug; drop the withdrawn one rather than leaving a
-- duplicate OpenCode entry in the catalog.
DELETE FROM agent_templates WHERE slug = 'qwen-code';

ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'custom'));
