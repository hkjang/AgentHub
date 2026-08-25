-- Runtime Pods may need to reach services bound to the Kubernetes node rather
-- than a ClusterIP. Preserve the requested checked-by-default behaviour for
-- upgraded installations while still allowing an administrator to save false.
UPDATE system_settings
SET value = jsonb_set(value, '{hostNetwork}', 'true'::jsonb, true)
WHERE key = 'kubernetes' AND NOT value ? 'hostNetwork';
