# Semantica and AgentHub: what fits, what it costs, and what to build first

Semantica (github.com/semantica-agi/semantica, MIT) calls itself "graph-native
infrastructure for context and accountable AI systems": ingestion into a
knowledge graph, W3C PROV-O lineage on every fact, deterministic reasoning
outside the model, and decisions as first-class nodes with causal ancestry.

That last part is the reason to look at it. This platform already records what
happened in enormous detail — a run's timeline, every step, every tool call and
the permission answered for it, the approval and who decided it, the DLP verdict,
the completion judgement, the metering — and almost none of it is *queryable as a
chain*. A person can open one run and read it. Nobody can ask "which decisions
did this policy change affect", "what has this agent concluded about this
customer, and on what basis", or "every run that used the image we retired on
Tuesday". Those are the questions an auditor asks, and the answer today is a
person reading rows.

## What was checked rather than assumed

- `pip install semantica` works; 0.6.6 imports on Python 3.12. MIT licence.
- Its MCP server exposes twelve tools, including exactly the decision-intelligence
  ones: `record_decision`, `query_decisions`, `find_precedents`,
  `get_causal_chain`, plus `add_entity`, `add_relationship`, `run_reasoning`,
  `export_graph` (turtle/nt/xml/json).
- The README shows `graph.record_decision(...)` on `KnowledgeGraph`. In the
  published 0.6.6 that class has **no decision methods at all** — the decision
  API exists on the MCP server, not the library. Integrate over MCP or the REST
  server, not by importing the package.
- The MCP server is **stdio only** (`main()` calls `_run_stdio()`), while this
  platform's fabric speaks `streamable-http` and the operator runs MCP servers as
  HTTP containers. That is the one real integration gap, and it is small.
- Storage can be embedded (Oxigraph, FAISS), which matters here: this platform is
  installed where there is no internet, and an integration that needs Neo4j
  reachable is an integration most of its deployments cannot use.

## The four ways they could join, cheapest first

**A. Register it in the MCP fabric, so agents can use it.** Agents get
`record_decision`, `find_precedents` and `get_causal_chain` as tools, bound
through the same bundles, tool policies and approval gates as any other server.
Cost: one container image wrapping the stdio server in streamable-http, plus a
network profile that reaches it. No control-plane code.

Its limit is worth stating plainly: what an agent records is what the agent
chose to say. It is the agent's account, subject to the same doubt as any other
thing an agent claims — this platform already refuses to treat "완료했습니다" as
proof.

**B. Export the platform's own account (recommended first).** Everything above is
already known to the control plane, and known *authoritatively*: the task and who
asked for it, the agent and its pinned version and image, the model endpoint, the
approval and the reviewer, the policy verdict, what the scanner refused, what the
evaluator decided and why. Push that after each run as a decision record with
PROV-O lineage, over Semantica's REST server. No MCP, no shim, no Python in the
control plane.

The shape already exists: this platform has an event bus with retries, dead
lettering and a replay window. A provenance sink is another subscriber, and if
Semantica is down the events wait rather than the run failing — which is the only
acceptable coupling for something on the path of real work.

**C. Deterministic reasoning over policy.** Semantica runs Datalog, Rete and
SHACL with an explanation for each conclusion. This platform's policy engine
answers allow/deny in Go, and its refusals already name the rule. The gain would
be explanation chains for compound policy; the cost is moving a security-critical
decision into a second engine. Not first, and not without a strong reason.

**D. The graph as a screen.** Semantica ships a knowledge explorer (React,
Sigma.js). A run's decision chain rendered as a graph belongs next to the run's
timeline. This is only worth building after B, because before B there is nothing
in the graph that the platform itself vouches for.

## What to build first, concretely

One subscriber, one event type, one mapping: on `task.completed` and
`task.failed`, post a decision record — category from the agent, scenario from
the task, reasoning from the evaluator's verdict, outcome from the run's result,
plus edges to the approval, the policy verdict and the model endpoint. Nothing
else changes. If it proves useful, extend to `approval.decided` and
`artifact.created`, both already published.

Two things to keep honest while building it. The record must say what the
platform observed, not what the agent asserted — those are different, and this
platform has spent a long time learning to separate them. And a deployment that
never configures a sink must be unaffected: no sink, no cost, no error.
