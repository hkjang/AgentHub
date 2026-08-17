-- Git workspaces could only clone public repositories: the clone ran with no
-- credential at all. A workspace now points at one of its owner's personal
-- secrets, which stays encrypted under that user's keyring and is handed to the
-- Runtime through its Secret rather than being copied here.
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS git_credential_secret_id text
  REFERENCES personal_secrets(id) ON DELETE SET NULL;

-- 'token' covers PAT-style HTTPS credentials, 'ssh-key' a private key. The
-- clone step picks its authentication method from this rather than guessing.
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS git_credential_kind text NOT NULL DEFAULT ''
  CHECK (git_credential_kind IN ('', 'token', 'ssh-key'));

-- Username for HTTPS token auth. Bitbucket expects the account name,
-- GitLab 'oauth2', GitHub anything; empty falls back to a safe default.
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS git_credential_username text NOT NULL DEFAULT '';
