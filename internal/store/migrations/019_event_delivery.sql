-- Event delivery: a lease, a retry budget, and a per-subscriber ledger.
--
-- The outbox was durable in the sense that events survived a restart, but the
-- dispatcher marked a batch dispatched in the same statement that claimed it.
-- Anything that went wrong afterwards — the worker dying, a subscriber lookup
-- failing, a task insert failing — lost the event: it was recorded as delivered
-- and nothing had been delivered. There was also no record of which subscriber
-- received which event, so a redelivery could not tell what it had already done.

-- Delivery is now attempted under a lease and only marked done when it finished.
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0;
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS claimed_by text NOT NULL DEFAULT '';
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS claimed_until timestamptz;
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
-- Out of attempts. Kept rather than deleted: an event nobody could deliver is
-- exactly the thing an operator needs to see.
ALTER TABLE platform_events ADD COLUMN IF NOT EXISTS dead_lettered_at timestamptz;

-- The pending index has to match what the claim now looks for.
DROP INDEX IF EXISTS platform_events_pending_idx;
CREATE INDEX IF NOT EXISTS platform_events_pending_idx
  ON platform_events(next_attempt_at)
  WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL;

-- One row per (event, subscriber): the ledger. Written in the same transaction
-- as the task it created, so a redelivery after a lost completion marker sees
-- that this subscriber already has its task and does not create a second one.
CREATE TABLE IF NOT EXISTS event_deliveries (
  event_id text NOT NULL REFERENCES platform_events(id) ON DELETE CASCADE,
  trigger_id text NOT NULL REFERENCES agent_triggers(id) ON DELETE CASCADE,
  task_id text REFERENCES agent_tasks(id) ON DELETE SET NULL,
  attempts integer NOT NULL DEFAULT 1,
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (event_id, trigger_id)
);

CREATE INDEX IF NOT EXISTS event_deliveries_trigger_idx ON event_deliveries(trigger_id, created_at DESC);
