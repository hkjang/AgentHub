-- The agent servers this deployment may send work to.
--
-- An OpenHands Agent Server is a REST API that runs agents on one machine, and a
-- site will have more than one: a development box, a machine inside the secure
-- network, one with a GPU. They are not runtimes this platform starts — they are
-- capacity it is given — so they are registered rather than spawned.
--
-- Registering one is an administrator's act. What it costs to get wrong is that
-- work leaves for a machine nobody meant it to reach, which is why the row
-- carries where it is allowed to be used from rather than only how to reach it.
CREATE TABLE IF NOT EXISTS agent_servers (
  id            text PRIMARY KEY,
  name          text NOT NULL,
  base_url      text NOT NULL,
  -- What kind of server this is. One kind today; named rather than assumed
  -- because the next one will not be OpenHands and a column added later cannot
  -- be filled in for rows that predate it.
  kind          text NOT NULL DEFAULT 'openhands' CHECK (kind IN ('openhands')),
  -- Which network this server sits in, as this deployment names its networks.
  -- It is free text on purpose: the platform does not get to decide what a
  -- site's zones are called.
  network_zone  text NOT NULL DEFAULT '',
  -- How many conversations this server may hold at once. Zero means the platform
  -- does not know, which is different from zero capacity and is why placement
  -- treats it as unbounded rather than as full.
  capacity      integer NOT NULL DEFAULT 0,
  enabled       boolean NOT NULL DEFAULT true,
  -- What the last health check found. Kept rather than asked on every read: a
  -- console listing ten servers must not make ten outbound calls, and an
  -- operator needs to see the last answer even when the server is now
  -- unreachable.
  health        text NOT NULL DEFAULT 'unknown' CHECK (health IN ('unknown','healthy','unreachable','refused')),
  health_detail text NOT NULL DEFAULT '',
  checked_at    timestamptz,
  created_by    text REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_servers_name_uq ON agent_servers (lower(name));
