-- One review agent, every pull request.
--
-- A review Goal named its two branches, so reviewing pull requests meant an
-- agent per branch — which is a person creating an agent for every proposal and
-- deleting it afterwards, or more likely not reviewing proposals at all.
--
-- In this mode the refs come from the task instead. A CI job posting to the
-- webhook trigger controls the body it sends, so it says which change to review
-- and the platform does not have to know GitHub's schema from GitLab's. What
-- that body must contain is checked at run time and named in the failure when it
-- is missing, rather than reviewing the wrong thing quietly.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_review_mode_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_review_mode_check
  CHECK (review_mode IN ('workspace', 'range', 'commit', 'scan', 'trigger'));
