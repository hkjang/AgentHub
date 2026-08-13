CREATE TABLE IF NOT EXISTS schema_migrations (
  version integer PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  username text NOT NULL,
  email text NOT NULL DEFAULT '',
  display_name text NOT NULL,
  password_hash text,
  oidc_subject text,
  role text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'manager', 'admin')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  manager_id text REFERENCES users(id) ON DELETE SET NULL,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_uq ON users(lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_uq ON users(lower(email)) WHERE email <> '';
CREATE UNIQUE INDEX IF NOT EXISTS users_oidc_subject_uq ON users(oidc_subject) WHERE oidc_subject IS NOT NULL;

CREATE TABLE IF NOT EXISTS sessions (
  id_hash bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  ip_address text,
  user_agent text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS system_settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL DEFAULT '{}'::jsonb,
  secret_value text,
  updated_by text REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_keyrings (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  version integer NOT NULL,
  encrypted_data_key text NOT NULL,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  retired_at timestamptz,
  UNIQUE(user_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS user_keyrings_active_uq ON user_keyrings(user_id) WHERE active;

CREATE TABLE IF NOT EXISTS personal_secrets (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL DEFAULT 'api_key',
  encrypted_value text NOT NULL,
  key_version integer NOT NULL,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, name)
);

CREATE TABLE IF NOT EXISTS api_keys (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  prefix text NOT NULL,
  token_hash bytea NOT NULL UNIQUE,
  scopes text[] NOT NULL DEFAULT ARRAY['api:read']::text[],
  expires_at timestamptz,
  last_used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runtime_profiles (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  cpu_millis integer NOT NULL CHECK (cpu_millis > 0),
  memory_mb integer NOT NULL CHECK (memory_mb > 0),
  storage_gb integer NOT NULL CHECK (storage_gb > 0),
  gpu_count integer NOT NULL DEFAULT 0 CHECK (gpu_count >= 0),
  idle_timeout_seconds integer NOT NULL DEFAULT 3600,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS security_profiles (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  spec jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS network_profiles (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  spec jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runtime_images (
  id text PRIMARY KEY,
  runtime_type text NOT NULL,
  name text NOT NULL,
  image text NOT NULL,
  version text NOT NULL,
  digest text NOT NULL DEFAULT '',
  sbom_uri text NOT NULL DEFAULT '',
  approved boolean NOT NULL DEFAULT false,
  deprecated boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(runtime_type, version)
);

CREATE TABLE IF NOT EXISTS model_endpoints (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  provider text NOT NULL,
  base_url text NOT NULL,
  default_model text NOT NULL,
  secret_value text,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mcp_servers (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  mode text NOT NULL CHECK (mode IN ('shared', 'dedicated', 'sidecar')),
  transport text NOT NULL DEFAULT 'streamable-http',
  endpoint text NOT NULL DEFAULT '',
  image text NOT NULL DEFAULT '',
  command jsonb NOT NULL DEFAULT '[]'::jsonb,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  risk_level text NOT NULL DEFAULT 'low' CHECK (risk_level IN ('low', 'medium', 'high', 'critical')),
  approval_required boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mcp_bundles (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  server_ids text[] NOT NULL DEFAULT ARRAY[]::text[],
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_templates (
  id text PRIMARY KEY,
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  category text NOT NULL DEFAULT 'general',
  runtime_type text NOT NULL CHECK (runtime_type IN ('opencode', 'hermes', 'custom')),
  runtime_profile_id text REFERENCES runtime_profiles(id),
  runtime_image_id text REFERENCES runtime_images(id),
  security_profile_id text REFERENCES security_profiles(id),
  network_profile_id text REFERENCES network_profiles(id),
  mcp_bundle_id text REFERENCES mcp_bundles(id),
  model_endpoint_id text REFERENCES model_endpoints(id),
  system_prompt text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  published boolean NOT NULL DEFAULT false,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspaces (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('empty', 'git', 'persistent', 'template', 'snapshot')),
  size_gb integer NOT NULL DEFAULT 10,
  repository_url text NOT NULL DEFAULT '',
  branch text NOT NULL DEFAULT '',
  pvc_name text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'ready',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, name)
);

CREATE TABLE IF NOT EXISTS workspace_snapshots (
  id text PRIMARY KEY,
  workspace_id text NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  storage_ref text NOT NULL DEFAULT '',
  size_bytes bigint NOT NULL DEFAULT 0,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_definitions (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  template_id text REFERENCES agent_templates(id) ON DELETE SET NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  runtime_type text NOT NULL CHECK (runtime_type IN ('opencode', 'hermes', 'custom')),
  runtime_profile_id text REFERENCES runtime_profiles(id),
  runtime_image_id text REFERENCES runtime_images(id),
  security_profile_id text REFERENCES security_profiles(id),
  network_profile_id text REFERENCES network_profiles(id),
  mcp_bundle_id text REFERENCES mcp_bundles(id),
  model_endpoint_id text REFERENCES model_endpoints(id),
  workspace_id text REFERENCES workspaces(id),
  system_prompt text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  spec jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id, name)
);

CREATE TABLE IF NOT EXISTS agent_runtimes (
  id text PRIMARY KEY,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'created',
  desired_state text NOT NULL DEFAULT 'stopped',
  crd_name text NOT NULL DEFAULT '',
  pod_name text NOT NULL DEFAULT '',
  node_name text NOT NULL DEFAULT '',
  endpoint text NOT NULL DEFAULT '',
  restart_count integer NOT NULL DEFAULT 0,
  failure_reason text NOT NULL DEFAULT '',
  last_activity_at timestamptz,
  started_at timestamptz,
  stopped_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_runtimes_owner_idx ON agent_runtimes(owner_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS runtime_sessions (
  id text PRIMARY KEY,
  runtime_id text NOT NULL REFERENCES agent_runtimes(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title text NOT NULL DEFAULT 'New session',
  status text NOT NULL DEFAULT 'active',
  trace jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS approvals (
  id text PRIMARY KEY,
  requester_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  reviewer_id text REFERENCES users(id) ON DELETE SET NULL,
  resource_type text NOT NULL,
  resource_id text NOT NULL,
  action text NOT NULL,
  reason text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  decided_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
  id bigserial PRIMARY KEY,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  actor_id text REFERENCES users(id) ON DELETE SET NULL,
  actor_name text NOT NULL DEFAULT 'system',
  action text NOT NULL,
  resource_type text NOT NULL DEFAULT '',
  resource_id text NOT NULL DEFAULT '',
  outcome text NOT NULL DEFAULT 'success',
  ip_address text NOT NULL DEFAULT '',
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS audit_events_time_idx ON audit_events(occurred_at DESC);

CREATE TABLE IF NOT EXISTS notifications (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type text NOT NULL,
  title text NOT NULL,
  message text NOT NULL DEFAULT '',
  resource_url text NOT NULL DEFAULT '',
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO runtime_profiles (id, name, description, cpu_millis, memory_mb, storage_gb, idle_timeout_seconds)
VALUES
  ('rp-tiny', 'Tiny', 'Tests and lightweight automations', 1000, 2048, 5, 1800),
  ('rp-basic', 'Basic', 'General-purpose agents', 2000, 4096, 10, 3600),
  ('rp-developer', 'Developer', 'Software development workspaces', 4000, 8192, 20, 3600),
  ('rp-advanced', 'Advanced', 'Large projects and research', 8000, 16384, 50, 7200)
ON CONFLICT (id) DO NOTHING;

INSERT INTO security_profiles (id, name, description, spec)
VALUES ('sp-restricted', 'Restricted', 'Non-root, no privilege escalation, no Kubernetes credentials',
  '{"runAsNonRoot":true,"readOnlyRootFilesystem":false,"allowPrivilegeEscalation":false,"dropCapabilities":["ALL"],"automountServiceAccountToken":false,"seccompProfile":"RuntimeDefault"}')
ON CONFLICT (id) DO NOTHING;

INSERT INTO network_profiles (id, name, description, spec)
VALUES ('np-restricted', 'Restricted', 'Only explicitly configured gateways and DNS are allowed',
  '{"defaultDeny":true,"allowDNS":true,"allowedDestinations":[]}')
ON CONFLICT (id) DO NOTHING;

INSERT INTO system_settings (key, value) VALUES
  ('general', '{"serviceName":"AgentHub","publicUrl":"","defaultLocale":"ko","timezone":"Asia/Seoul"}'),
  ('authentication', '{"localLoginEnabled":true,"oidcEnabled":false,"issuerUrl":"","clientId":"","scopes":["openid","profile","email"],"usernameClaim":"preferred_username","groupsClaim":"groups","adminGroups":[]}'),
  ('kubernetes', '{"enabled":false,"namespace":"agent-runtime-dev","mode":"inCluster","apiServer":"","verifyTls":true,"crdEnabled":true}'),
  ('governance', '{"teamApprovalEnabled":false,"highRiskToolApproval":true,"defaultIdleTimeoutSeconds":3600,"maxRuntimesPerUser":3,"maxCpuMillisPerUser":12000,"maxMemoryMbPerUser":32768,"maxStorageGbPerUser":100}'),
  ('logging', '{"level":"info","retentionDays":30,"includeRuntimeLogs":true}'),
  ('release', '{"offlineMode":true,"updateCheckEnabled":false}')
ON CONFLICT (key) DO NOTHING;
