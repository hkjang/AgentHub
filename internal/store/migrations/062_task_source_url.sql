-- Where a task came from, when something outside the platform started it.
--
-- A webhook-started review knew what to look at and nothing about where the
-- request came from: the pull request was named in the payload and dropped on
-- the floor. Somebody reading the finished review had no way back to the change
-- being reviewed except by searching for the branch by hand.
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS source_url text NOT NULL DEFAULT '';
