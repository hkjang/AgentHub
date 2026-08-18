-- Approval enforced where the tool call happens.
--
-- Approval was advisory: the goal asked the agent to declare a state-changing
-- action and wait, so an agent that simply called the tool went around the gate.
-- The MCP servers marked "approval required" in the admin catalogue, and the
-- high-risk-tool-approval governance switch, did nothing at all.
--
-- The in-Pod egress gateway now holds the call until a person decides, which is
-- the only place an agent cannot route around.

-- Tools that need a decision before they run, per agent and server. Separate
-- from the allow/deny list: a blocked tool never runs, a gated tool runs once
-- somebody says so.
ALTER TABLE mcp_tool_policies ADD COLUMN IF NOT EXISTS approval_tools text[] NOT NULL DEFAULT '{}';

-- The gateway authenticates to the control plane with the runtime's own token.
-- Only its hash is stored: the token itself lives in the Pod's Secret, and a
-- database copy would be a second place to steal it from.
ALTER TABLE agent_runtimes ADD COLUMN IF NOT EXISTS gateway_token_hash bytea;

-- One row per gated call, so a decision can be found again by the gateway that
-- is waiting for it and an operator can see what was asked.
CREATE TABLE IF NOT EXISTS tool_approvals (
  id text PRIMARY KEY,
  approval_id text NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  runtime_id text NOT NULL REFERENCES agent_runtimes(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  server_name text NOT NULL,
  tool_name text NOT NULL,
  -- A trimmed rendering of the arguments: the reviewer has to see what the call
  -- would do, and the full payload can be large or carry a secret.
  arguments text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tool_approvals_runtime_idx ON tool_approvals(runtime_id, created_at DESC);
