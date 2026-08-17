-- MCP servers were bound to runtimes with only name/mode/endpoint/image/port, so
-- any server behind authentication could not be reached at all. These columns
-- describe how the runtime should authenticate; the credential itself is stored
-- in the encrypted secret table rather than here.
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS auth_type text NOT NULL DEFAULT 'none'
  CHECK (auth_type IN ('none', 'bearer', 'header', 'basic'));

-- Header name for auth_type='header'; ignored otherwise. Bearer and basic use
-- the standard Authorization header.
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS auth_header text NOT NULL DEFAULT '';

-- When set, each user supplies their own credential through Secrets & API keys
-- and the platform-wide credential below is not used.
ALTER TABLE mcp_servers ADD COLUMN IF NOT EXISTS per_user_credential boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS mcp_credentials (
  id text PRIMARY KEY,
  server_id text NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
  -- NULL owner means the shared credential an administrator configured for the
  -- server; a set owner is that user's personal credential for the same server.
  owner_id text REFERENCES users(id) ON DELETE CASCADE,
  ciphertext text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS mcp_credentials_shared_idx
  ON mcp_credentials (server_id) WHERE owner_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS mcp_credentials_owner_idx
  ON mcp_credentials (server_id, owner_id) WHERE owner_id IS NOT NULL;
