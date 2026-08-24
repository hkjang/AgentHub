-- How the platform talks back to a forge.
--
-- A review that came from a pull request now knows which one, and could say what
-- it found where the change is being discussed rather than only inside AgentHub.
-- Doing that needs a credential for the host, kept the way every other outbound
-- credential here is kept: encrypted, never read back, and belonging to a person
-- rather than to the platform.
--
-- One connection per owner per host. A second token for the same host is a
-- replacement, not an addition: two credentials for one host is a question about
-- which one applies, and the honest answer is that nobody knows.
CREATE TABLE IF NOT EXISTS scm_connections (
    id          text PRIMARY KEY,
    owner_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('github','gitlab','gitea','bitbucket')),
    api_base    text NOT NULL DEFAULT '',
    ciphertext  text NOT NULL,
    last_used_at timestamptz,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, host)
);
