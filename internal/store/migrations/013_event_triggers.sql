-- Event triggers: an agent reacts to what happens on the platform.
--
-- Events are written to an outbox rather than delivered in process, because the
-- API publishes them and the worker dispatches them. That keeps delivery
-- durable across a restart and safe with several workers, which an in-memory
-- bus could not offer in an offline deployment with no broker to lean on.

ALTER TABLE agent_triggers DROP CONSTRAINT IF EXISTS agent_triggers_type_check;
ALTER TABLE agent_triggers
  ADD CONSTRAINT agent_triggers_type_check CHECK (type IN ('manual', 'cron', 'webhook', 'event'));

-- Event only. The type to react to, and an optional equality filter over the
-- event payload so one agent can watch a single runtime rather than all of them.
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS event_type text NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS event_filter jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS agent_triggers_event_idx
  ON agent_triggers(owner_id, event_type) WHERE enabled AND type = 'event';

CREATE TABLE IF NOT EXISTS platform_events (
  id text PRIMARY KEY,
  type text NOT NULL,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject_type text NOT NULL DEFAULT '',
  subject_id text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- The trigger that caused the work this event reports on, if any. A trigger
  -- never fires on an event it caused, which is what stops an agent from
  -- retriggering itself forever.
  cause_trigger_id text REFERENCES agent_triggers(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  dispatched_at timestamptz
);

CREATE INDEX IF NOT EXISTS platform_events_pending_idx
  ON platform_events(created_at) WHERE dispatched_at IS NULL;
CREATE INDEX IF NOT EXISTS platform_events_owner_idx ON platform_events(owner_id, created_at DESC);

ALTER TABLE agent_tasks DROP CONSTRAINT IF EXISTS agent_tasks_source_check;
ALTER TABLE agent_tasks
  ADD CONSTRAINT agent_tasks_source_check CHECK (source IN ('manual', 'cron', 'webhook', 'agent', 'event'));
