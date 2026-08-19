-- Operating the execution plane.
--
-- Two things were missing for anyone running this in production. There was no
-- record of which workers exist, so "no worker is running" could only be guessed
-- from whether any task happened to be claimed — a quiet queue and a dead worker
-- looked identical. And a task claimed by a worker that then died stayed
-- 'running' forever: the claim carried a lease, but nothing ever reaped one, so
-- the row was stranded where no claim query would look at it again.

-- One row per worker process, refreshed by its own heartbeat.
CREATE TABLE IF NOT EXISTS execution_workers (
  id text PRIMARY KEY,
  hostname text NOT NULL DEFAULT '',
  version text NOT NULL DEFAULT '',
  -- The floor and ceiling this worker was started with, so an operator can see
  -- the capacity that is actually deployed rather than the capacity intended.
  concurrency integer NOT NULL DEFAULT 0,
  max_concurrency integer NOT NULL DEFAULT 0,
  -- running is what it reported holding at its last heartbeat.
  running integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'paused', 'stopped')),
  started_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS execution_workers_seen_idx ON execution_workers(last_seen_at DESC);
