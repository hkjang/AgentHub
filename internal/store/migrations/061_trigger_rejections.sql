-- What a webhook trigger refused, kept where its owner can see it.
--
-- Every rejection — a wrong signature, a missing header, a replayed delivery —
-- wrote a line in the server log and nothing else. So a sender that has been
-- calling with the wrong secret for two days looks exactly like a trigger nobody
-- has wired up yet: the caller sees 401, the owner sees an empty trigger, and
-- neither of them can see the other's half.
--
-- Counters on the trigger row rather than a row per attempt. A table would grow
-- with whatever traffic an unauthenticated endpoint attracts, which is the one
-- shape of history that must not be created by strangers.
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS rejected_count integer NOT NULL DEFAULT 0;
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS last_rejection text NOT NULL DEFAULT '';
ALTER TABLE agent_triggers ADD COLUMN IF NOT EXISTS last_rejected_at timestamptz;
