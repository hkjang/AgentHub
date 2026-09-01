-- Prime Agent joins the runtime types.
--
-- It shares a core with the Pi runtime already here, but it is a separate
-- distribution on its own release line, and it carries the one thing Pi's npm
-- build does not: an Agent Client Protocol mode. That is a backend this platform
-- already has, so the agent arrives driveable rather than needing execution code.
--
-- Two tables name the list, and widening one without the other is a runtime that
-- can be created and never templated, or offered in the catalog and refused on
-- save.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
    CHECK (runtime_type IN ('opencode','hermes','qwenpaw','qwencode','goose','holmes','browsercode',
                            'jupyter','langflow','nodered','n8n','opencodereview','orca','pi',
                            'primeagent','openhands','custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
    CHECK (runtime_type IN ('opencode','hermes','qwenpaw','qwencode','goose','holmes','browsercode',
                            'jupyter','langflow','nodered','n8n','opencodereview','orca','pi',
                            'primeagent','openhands','custom'));
