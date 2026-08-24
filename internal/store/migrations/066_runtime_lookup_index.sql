-- Two lookups on agent_runtimes had no index to answer them, and both are on
-- paths that run constantly rather than when somebody opens a screen.
--
-- The first is how a runtime proves who it is. Every call a Pod makes back to
-- this control plane — a model request through the gateway, a tool waiting on an
-- approval — arrives with a token and is answered by scanning agent_runtimes for
-- its hash. Authentication was the most frequent full scan this platform
-- performs, and it grew with every runtime ever created, since the row outlives
-- the Pod.
--
-- Measured on 200,000 runtime rows, 20,000 of them holding a token:
--   without   16.4  13.1  12.8 ms
--   with       1.9   0.21  0.21 ms
CREATE INDEX IF NOT EXISTS agent_runtimes_gateway_idx
  ON agent_runtimes(gateway_token_hash) WHERE gateway_token_hash IS NOT NULL;

-- The second is "what is this agent's current runtime", which every task that
-- needs a runtime asks before it starts, and five screens ask besides. It reads
-- the newest row for one agent, and read it by scanning all of them.
--
-- Same table, 5,000 agents:
--   without   17.5  14.1  13.1 ms   parallel sequential scan
--   with       2.5   0.27  0.26 ms  index scan, one row
--
-- Ordered and filtered exactly as the query is, so the answer is the first entry
-- rather than a sort of everything that agent has ever run.
CREATE INDEX IF NOT EXISTS agent_runtimes_agent_idx
  ON agent_runtimes(agent_id, created_at DESC) WHERE desired_state <> 'deleted';
