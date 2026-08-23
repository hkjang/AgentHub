-- Something to say to a run that is already going.
--
-- A protocol-backed agent can be redirected, asked a follow-up and interrupted
-- while it works, but the person who wants to do that is at a browser and the
-- conversation is held by a worker process somewhere else. This table is the
-- path between them: the API writes what somebody said, the worker delivers it
-- into the conversation it is holding.
--
-- It follows the shape cancellation already uses — the worker notices between
-- events rather than being interrupted — because a directive that arrived in the
-- middle of writing a message would corrupt the very conversation it is meant to
-- steer.
CREATE TABLE IF NOT EXISTS run_directives (
  id           text PRIMARY KEY,
  run_id       text NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
  -- What kind of thing this is to say. 'steer' changes the direction of work in
  -- progress; 'follow_up' is queued for when the current work is done.
  kind         text NOT NULL CHECK (kind IN ('steer', 'follow_up')),
  message      text NOT NULL,
  created_by   text REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- When the worker actually put it into the conversation. Null means nobody has
  -- said it yet, which is the difference between "asked for" and "delivered" —
  -- and a console that showed the first as the second would be lying about
  -- whether the agent had heard.
  delivered_at timestamptz,
  -- What the agent said back about it. A directive a protocol refused is not a
  -- directive that was delivered.
  outcome      text NOT NULL DEFAULT ''
);

-- The worker's own question: what is there to say to this run that nobody has
-- said yet.
CREATE INDEX IF NOT EXISTS run_directives_pending_idx
  ON run_directives (run_id, created_at) WHERE delivered_at IS NULL;
