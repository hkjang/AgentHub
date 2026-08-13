UPDATE security_profiles
SET spec = jsonb_set(spec, '{readOnlyRootFilesystem}', 'true'::jsonb, true),
    updated_at = now()
WHERE id = 'sp-restricted';

UPDATE agent_definitions
SET security_profile_id = 'sp-restricted'
WHERE security_profile_id IS NULL;

UPDATE agent_definitions
SET network_profile_id = 'np-restricted'
WHERE network_profile_id IS NULL;
