-- Event notifications leave the building over the company SMTP relay, and every
-- attempt is written here first: an event queues a row and returns, a sender in
-- the control plane delivers it in the background, and the row keeps whether it
-- went. "Did my approval mail go out" is answered from this table — which is
-- why failures alone would not do, and why there is no body column: the subject
-- and the recipient say what left, without the record becoming a copy of it.
CREATE TABLE IF NOT EXISTS mail_deliveries (
  id text PRIMARY KEY,
  event text NOT NULL,
  user_id text REFERENCES users(id) ON DELETE SET NULL,
  recipient text NOT NULL,
  subject text NOT NULL DEFAULT '',
  resource_url text NOT NULL DEFAULT '',
  actor_id text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sending','sent','failed')),
  attempts integer NOT NULL DEFAULT 0,
  error_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  next_attempt_at timestamptz NOT NULL DEFAULT now()
);
-- The sender asks for what is due, oldest first; the screen asks for the
-- newest. A partial index over the unsent rows answers the first without
-- growing with the history.
CREATE INDEX IF NOT EXISTS mail_deliveries_due_idx ON mail_deliveries(next_attempt_at) WHERE status IN ('queued','sending');
CREATE INDEX IF NOT EXISTS mail_deliveries_created_idx ON mail_deliveries(created_at DESC);
