-- The setting is named for the backend it was built for, and it now serves three.
--
-- `cli_approval_mode` arrived with the headless runner, where it is handed to the
-- agent and the agent decides for itself. The ACP backend then used it as the
-- policy the platform answers requests with, and the investigator uses it to
-- decide whether shell commands are allowed at all. Three meanings is fine —
-- they are the same question, how much may this run do without asking — but a
-- name that says `cli` for all three sends a reader looking for a command line
-- that two of them do not have.
ALTER TABLE agent_goals RENAME COLUMN cli_approval_mode TO approval_mode;
