-- OpenHands joins the runtime types.
--
-- It was already an execution backend, but only against a server somebody else
-- installed and registered by URL. As a runtime type the platform can start one
-- itself, which is what makes it testable the way every other runtime here is.
--
-- Two tables name the list, and widening one without the other is a runtime that
-- can be created and never templated, or offered in the catalog and refused on
-- save.
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
    CHECK (runtime_type IN ('opencode','hermes','qwenpaw','qwencode','goose','holmes','browsercode',
                            'jupyter','langflow','nodered','n8n','opencodereview','orca','pi','openhands','custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
    CHECK (runtime_type IN ('opencode','hermes','qwenpaw','qwencode','goose','holmes','browsercode',
                            'jupyter','langflow','nodered','n8n','opencodereview','orca','pi','openhands','custom'));
