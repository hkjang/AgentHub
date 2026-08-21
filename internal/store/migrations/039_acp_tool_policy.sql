-- A named tool policy for ACP runs.
--
-- The approval mode judges by the kind of tool the agent declares, which some
-- agents do not distinguish: Goose and BrowserCode report `other` for nearly
-- everything, so a mode that reads kinds is all-or-nothing for them. Names are
-- what an operator actually wants to talk about — never `rm -rf`, always
-- `npm test` — so a goal may carry two lists that are matched against the tool's
-- own title before the mode is consulted.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS tool_policy jsonb NOT NULL DEFAULT '{}'::jsonb;
