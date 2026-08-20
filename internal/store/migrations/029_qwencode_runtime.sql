-- Qwen Code joins the runtime adapters, and the withdrawn `qwencode` value comes
-- back — this time with something behind it.
--
-- Migration 008 removed it because it had no launch behaviour of its own: it
-- started the OpenCode server and was therefore the same runtime under a second
-- name. What returns here is the actual product: a terminal agent with its own
-- tool loop, started as itself, published through a browser terminal, given its
-- own settings file with the bound MCP servers in it, and drivable headlessly so
-- an autonomous task can run that tool loop instead of only describing it.
--
-- Rows never kept the old value: 008 remapped every one of them to `opencode`
-- and nothing has been able to write `qwencode` since, so this only widens the
-- constraints. Keep them, internal/runtimetype.Supported and the AgentRuntime
-- CRD enum in step.

ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_runtime_type_check;
ALTER TABLE agent_definitions ADD CONSTRAINT agent_definitions_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'langflow', 'custom'));

ALTER TABLE agent_templates DROP CONSTRAINT IF EXISTS agent_templates_runtime_type_check;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_runtime_type_check
  CHECK (runtime_type IN ('opencode', 'hermes', 'qwenpaw', 'qwencode', 'langflow', 'custom'));

-- Where the work happens for a Qwen Code agent: `cli` runs the runtime's own
-- agent headlessly. The approval mode is stored beside it because it decides
-- whether an unattended run may change files without asking, which is the single
-- most consequential setting on this runner and must not be implicit.
ALTER TABLE agent_goals DROP CONSTRAINT IF EXISTS agent_goals_runner_check;
ALTER TABLE agent_goals ADD CONSTRAINT agent_goals_runner_check
  CHECK (runner IN ('prose', 'flow', 'cli'));

ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS cli_approval_mode text NOT NULL DEFAULT 'default'
  CHECK (cli_approval_mode IN ('plan', 'default', 'auto-edit', 'auto', 'yolo'));
