CREATE TABLE IF NOT EXISTS runtime_launch_tickets (
  id_hash bytea PRIMARY KEY,
  runtime_id text NOT NULL REFERENCES agent_runtimes(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS runtime_launch_tickets_expiry_idx ON runtime_launch_tickets(expires_at);

INSERT INTO system_settings (key, value)
VALUES ('sessionGateway', '{"enabled":false,"scheme":"https","baseDomain":"","sessionHours":8}')
ON CONFLICT (key) DO NOTHING;
