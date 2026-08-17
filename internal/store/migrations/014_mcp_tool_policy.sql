-- MCP tool policy: which tools of a bound MCP server an agent may actually call.
--
-- Binding a bundle already decides which servers an agent reaches, but a server
-- is not a permission boundary: one MCP server commonly exposes both a harmless
-- lookup and a destructive write. The policy is enforced by an egress gateway in
-- the Pod, not by the agent process, so it holds whatever the model decides to
-- try.

CREATE TABLE IF NOT EXISTS mcp_tool_policies (
  id text PRIMARY KEY,
  agent_id text NOT NULL REFERENCES agent_definitions(id) ON DELETE CASCADE,
  server_id text NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
  -- allow lists exactly what may be called; deny blocks the listed tools and
  -- permits the rest.
  mode text NOT NULL CHECK (mode IN ('allow', 'deny')),
  tools text[] NOT NULL DEFAULT '{}',
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (agent_id, server_id)
);

CREATE INDEX IF NOT EXISTS mcp_tool_policies_agent_idx ON mcp_tool_policies(agent_id);
