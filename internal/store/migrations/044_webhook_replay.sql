-- A webhook signature proves the sender knew the secret. It does not prove this
-- is the first time the request has been sent: the signature is a function of the
-- body, so a captured request stays valid forever and every replay of it queues
-- another task. Anyone who sees one request — in a proxy log, a CI transcript, a
-- shared curl command — can fire that agent again whenever they like.
--
-- The delivery is claimed by its signature. A body that carries anything unique
-- (a delivery id, a timestamp, an event id — most senders include one) signs
-- differently every time and is unaffected; an identical body a second time is a
-- replay, and a sender retrying the identical request after a timeout gets the
-- at-most-once behaviour it wanted anyway.
CREATE TABLE IF NOT EXISTS webhook_deliveries (
  trigger_id  text NOT NULL REFERENCES agent_triggers(id) ON DELETE CASCADE,
  signature   text NOT NULL,
  task_id     text REFERENCES agent_tasks(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (trigger_id, signature)
);

-- The sweep reads this; without it the delete is a sequential scan over every
-- delivery the platform has ever accepted.
CREATE INDEX IF NOT EXISTS webhook_deliveries_created_idx ON webhook_deliveries (created_at);
