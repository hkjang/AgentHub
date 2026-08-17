# AgentHub architecture

AgentHub is the control plane. Agent processes never execute inside the Portal container.

```text
Browser ──> AgentHub API / Session Gateway ──> PostgreSQL
                 │
                 ├──> AgentRuntime CRD
                 │          │
                 │          v
                 └──> Agent Operator ──> StatefulSet + Service + PVC
                                                │
                              ┌─────────────────┴─────────────────┐
                              v                                   v
                     OpenCode :4096           Hermes API :8642 + UI :9119
```

The persistent `agent_definitions` record is intentionally separate from an
`agent_runtimes` instance. Stopping a runtime scales its StatefulSet to zero;
the Agent definition and Workspace PVC remain.

## Security boundaries

- Browser sessions are random, hashed in PostgreSQL, `HttpOnly`, `SameSite=Lax`, and protected by a separate CSRF token.
- Local passwords use bcrypt. Keycloak tokens are verified using OIDC discovery, issuer, audience, signature, expiry, state, and PKCE.
- The only bootstrap configuration read from environment variables is the PostgreSQL DSN, bootstrap administrator identity/password, and 32-byte encryption key.
- OIDC client secrets and external Kubernetes tokens use AES-256-GCM with authenticated context binding.
- Each user owns a random data-encryption key wrapped by the deployment encryption key. Rotation re-encrypts all personal secrets transactionally.
- Runtime Pods are non-root, use RuntimeDefault seccomp, drop all Linux capabilities, disallow privilege escalation, have a read-only root filesystem, and never receive a Kubernetes ServiceAccount token.
- A default-deny NetworkPolicy permits only the runtime ports from the control-plane namespace. DNS, selected Model/MCP endpoint ports, and administrator-approved CIDR/port destinations form the generated egress allow-list.
- API keys are shown once and only their SHA-256 digest is persisted.
- Audit records never contain secret plaintext.

## Browser session gateway

OpenCode's native Web UI uses origin-root assets and APIs, so it cannot be
reliably mounted under a Portal subpath. AgentHub issues a two-minute, one-use
launch ticket and sends the browser to a Runtime-specific origin such as
`<runtime-id>.agents.company.local`. The ticket is exchanged for an encrypted,
host-only, `HttpOnly` Runtime cookie. Portal cookies and upstream cookies are
never forwarded across this boundary; the gateway injects the per-Runtime
credential server-side and audits the launch.

Hermes Dashboard runs only on Pod loopback (`127.0.0.1:9120`). A small
`agenthub-runtime-proxy` sidecar exposes port 9119, checks the per-Runtime token,
and reports ready only after the Dashboard responds. NetworkPolicy then limits
that port to AgentHub's control-plane namespace.

## Runtime adapter contract

The Portal implements a common `Spawner` interface. Kubernetes is the default
implementation and creates an `AgentRuntime` resource. Runtime-specific launch
behavior lives in the offline `agenthub-base` image:

- `opencode`: `opencode serve --hostname 0.0.0.0 --port 4096`, authenticated with `OPENCODE_SERVER_PASSWORD`
- `hermes`: `hermes gateway run --no-supervise` with its authenticated API on 8642 and the loopback Dashboard/proxy pair on 9120/9119
- `qwenpaw`: `qwenpaw app --host 0.0.0.0 --port 8642`; the app ships no authenticator, so it is published only through the `agenthub-runtime-proxy` sidecar on 9119
- `custom`: an administrator-approved image and command

Runtime UIs are proxied verbatim: the session gateway authenticates, rewrites
redirects and strips cookies, but never rewrites the response body.

Each adapter is registered in `internal/operator/adapter.go` as a `runtimeAdapter`
describing its start command, extra environment, init containers and sidecars.
Adding a runtime means registering one of those rather than threading another
branch through the StatefulSet builder.

`internal/runtimetype` is the single source of truth for this list. Changing it
means updating the CRD enum in `deploy/kubernetes/crd.yaml` and the
`runtime_type` CHECK constraints in the initial migration to match.

The Operator compiles each selected MCP Bundle into native runtime configuration:

- Shared endpoints are injected as remote MCP servers.
- Sidecar mode adds a restricted container to the Agent Pod and routes the
  runtime to its loopback Streamable HTTP endpoint.
- Dedicated mode creates a separately isolated StatefulSet, Service, and
  NetworkPolicy owned by the same `AgentRuntime` CRD.

OpenCode receives the generated configuration through `OPENCODE_CONFIG`;
Hermes receives a generated `config.yaml` under its isolated `HERMES_HOME`;
QwenPaw is initialised with `qwenpaw init --defaults` and receives the model
binding as an `.env` file under `QWENPAW_HOME`.

## Workspace and home persistence

Two volumes back every runtime. The workspace PVC holds the user's project files
and is deliberately preserved when an agent is deleted. A second, runtime-owned
PVC backs `/home/agent`, where the adapters keep their own state — QwenPaw's
provider registry and skill pool, Hermes' memory, OpenCode's settings. That was
an emptyDir until v0.5.0, which discarded the user's setup on every Pod
recreation; because a configuration change now rolls the Pod, that happened
often.

Private repositories are cloned with a credential the workspace is bound to: one
of the owner's personal secrets, either an HTTPS token or an SSH private key. The
value reaches the Pod through the runtime Secret and is written to a 0600 file on
the tmpfs, never onto a command line or into the remote URL.

## Multi-agent workflows

`internal/workflow` executes a saved graph. Steps run in topological levels
bounded by the workflow's own guardrails — depth, agent calls, parallelism and a
wall-clock deadline — and each step receives the run input plus the outputs of
the steps it depends on, attributed to the agent that produced them. Router mode
lets the entry step name the branch to follow and skips the rest.

A step runs its agent's system prompt against that agent's model endpoint. This
is model-level orchestration: tool use inside a single step belongs to the
runtime adapters and is reached through their browser sessions. Every run is
persisted with its per-step trace, latency and token accounting, and correlated
to the originating request through a trace id that appears in both the response
and the structured logs.

## Agent Execution Plane

Agents can be driven two ways at once. The interactive path is unchanged: create
an agent, spawn its Runtime, open the browser session and work in it. The
autonomous path adds a Goal and Triggers, and the platform does the rest.

    Trigger → AgentTask → Worker claims → AgentRun → reasoning steps
            → completion evaluated → artifacts stored → Runtime released

`internal/execution` owns that flow. A Task is queued in PostgreSQL and claimed
with `FOR UPDATE SKIP LOCKED` under a lease, so several `agenthub-worker`
replicas share one queue and a worker that dies releases its task rather than
stranding it. The API process never executes an agent: a run can take minutes,
which does not belong in a request handler.

Runtimes are acquired through the same Runtime Manager the interactive path uses,
never a parallel implementation. A Runtime the user already had running is reused
and never stopped by a task; only one the task started itself is released, and
only when the agent's policy says so. That is what lets someone open the live
Runtime while a task is working in it.

Completion is decided by the platform, not claimed by the agent. `agent` trusts
the declaration, `rule` requires every success criterion to be evidenced in the
transcript, `judge` has a separate model call assess it, and `composite` requires
both. A judge that cannot be reached fails the task rather than passing it. The
verdict is stored on the run, so a completed task can be defended later.

Only infrastructure failures are retried, with exponential backoff; an agent that
did not meet its goal will not meet it by being asked again unprompted. A task
that exhausts its retries lands in `dead_letter` for an operator instead of
retrying forever.

Cron schedules are parsed in-process rather than through a dependency, and the
worker that advances `next_fire_at` first owns that firing, so a schedule fires
once no matter how many workers are running. Webhook triggers verify an HMAC over
the raw body; it is the only unauthenticated route in the API.

Event triggers let an agent react to what happens on the platform — a task that
failed, a runtime that crashed, an approval that was decided, an artifact that
was produced. Events go to a durable outbox in PostgreSQL rather than an
in-process bus: the API publishes some of them and the worker dispatches all of
them, an offline site has no broker to lean on, and a restart must not drop what
was in flight. The dispatcher claims a batch and marks it delivered in the same
statement, so every worker can run one without anything being delivered twice.

Two things keep event triggers from becoming a feedback loop. A subscription
carries an optional payload filter, applied as jsonb containment in SQL, so an
agent watching one runtime is not woken by every other runtime's failures. And
every event records the trigger that caused the work it reports on; a trigger
never fires on an event its own task produced, which is what stops an agent from
waking itself forever. Publishing is best-effort throughout: the task already
finished, and failing it because nothing could be told about it would be worse
than a missed trigger.

## Custom runtimes

Three runtimes ship with adapters. A fourth type, `custom`, has none by design:
it is how a site runs an agent AgentHub has never heard of. Its definition
carries what the adapter would otherwise supply — the start command, one
argument per element as in a Kubernetes container spec, and the port it serves
on. There is no shell in between, so there is no quoting to get wrong.

A custom runtime with no command is refused when the definition is saved and
again when the operator parses the object. Both checks exist because the failure
they prevent is silent: the container would run its image's default entrypoint
and crash-loop with nothing in the status explaining why.

## Autonomous control

Four controls sit on top of the execution plane. They exist because an agent that
runs unattended needs bounds that an interactive session gets from the person
sitting in front of it.

**Planning** is per-agent, not global. `native` leaves planning to the runtime
adapter, which is right for OpenCode and Hermes because they already run their
own agent loops; `platform` has AgentHub produce a plan first and store it beside
the run; `hybrid` does both. A planner that cannot be reached does not stop the
work — the plan is an aid, not a gate.

**Approval** is the gate on state-changing action. The agent declares intent with
an `APPROVAL` directive, the task moves to `waiting_approval`, and it stays out of
the queue until a reviewer decides. Waiting is not a failed attempt, so the
attempt counter is rolled back on resume; otherwise a slow approval would eat the
retry budget. The decision — approved or rejected, with its reason — is written
back into the transcript, so the resumed run knows what it may do. A rejection
ends the task and is never retried, because a refusal is a decision.

**Memory** is stored in PostgreSQL rather than the Runtime's home directory, so
what an agent learns outlives the Pod. Entries are keyed per scope (`agent`,
`task`, `workspace`) with a unique index, so rewriting a key replaces it instead
of accumulating duplicates, and each value is capped so one remembered fact
cannot crowd out the prompt.

**Delegation** always goes through the task queue, never a direct call into
another agent's Runtime — that is what keeps permissions, quota, depth and the
audit trail intact. Depth is bounded per agent (`0` forbids delegation), and the
parent chain is walked before the child task is created so that A → B → C → A is
refused. A refused delegation is reported back to the delegating agent in its
transcript rather than silently dropped, so it can carry on knowing what was
handed off and what was not.

Directives are parsed from fenced blocks (`<<<KIND arg … >>>`). An unterminated
block, an unknown kind, or prose that merely describes the protocol yields
nothing, so an agent explaining how approvals work cannot request one.

## MCP tool policy

Binding a bundle decides which MCP servers an agent reaches, but a server is not
a permission boundary: one MCP server commonly exposes a harmless lookup and a
destructive write side by side. A tool policy narrows that to named tools, in
either direction — `allow` lists exactly what may be called, `deny` blocks the
listed tools and permits the rest. An `allow` policy with an empty list permits
nothing, because the alternative reading turns a misconfiguration into open
access.

The policy is enforced by an egress gateway in the Pod, not by the agent. A
policied binding's generated configuration points at `127.0.0.1:9129/mcp/<name>`
and the real upstream address is known only to the gateway, so the agent process
cannot route around the policy even if the model decides to try. The gateway
refuses a forbidden `tools/call` with a JSON-RPC error — a transport 200, since
the exchange itself succeeded — and filters `tools/list` so a tool that cannot be
called is not advertised either; otherwise the model keeps planning around
something that always fails. Streamable HTTP answers with either JSON or SSE, so
both framings are filtered. Every decision, allowed or denied, is logged.

The credential moves with the enforcement: for a policied binding it is mounted
into the gateway container and attached on the way out, so it never enters the
agent container at all. A binding with no policy still talks to its server
directly and keeps its credential in the runtime, which is what makes adopting
this incremental rather than a flag day.

AgentHub's own sidecars — this gateway and the session proxy — run the control
plane's image (`AGENTHUB_SIDECAR_IMAGE`), not the runtime image. Pinning an agent
to an older runtime image is a supported way to keep a definition reproducible;
it must not also pin the platform's code, which once meant a policy shipped in a
new release crash-looping inside an old Pod.
