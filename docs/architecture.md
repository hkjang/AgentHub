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

### Without a runtime base domain

A wildcard DNS name and a wildcard certificate are a real prerequisite, and a
deployment that does not have them yet used to be unable to open a workspace at
all. So when no Runtime Base Domain is configured, the same session is served
from the Portal's own origin under `/{runtimeId}/`. The launch flow is unchanged
— the same one-use ticket, exchanged for the same kind of encrypted `HttpOnly`
session cookie — and the path prefix travels upstream as `X-Forwarded-Prefix`
with redirects rewritten back under it.

Two details make the shared origin work:

- The first path segment must be a runtime UUID, which is what keeps this from
  shadowing a Portal route, a static asset or `/api/...`.
- A runtime UI that asks for `/assets/...` from the origin root is recognised by
  its `Referer`: a request made from a `/{runtimeId}/` page is routed to that
  runtime, and a request from a Portal page stays with the Portal. The session
  cookie is named per runtime, so several open workspaces do not overwrite each
  other's session. A request that carries no `Referer` at all — a WebSocket
  handshake, for instance — cannot be attributed and stays with the Portal.

An origin per runtime remains the recommended setup, because it is what keeps a
runtime's UI out of the Portal's origin. This mode is a way to work before that
DNS exists, not a replacement for it.

Hermes Dashboard runs only on Pod loopback (`127.0.0.1:9120`). A small
`agenthub-runtime-proxy` sidecar exposes port 9119, checks the per-Runtime token,
and reports ready only after the Dashboard responds. NetworkPolicy then limits
that port to AgentHub's control-plane namespace.

## Verifying the path the platform takes

Three runtimes were released unable to start. Each had been verified — the image
built, the agent spoke its protocol, a Pod came up under the restricted security
profile — and every one of those Pods was written by hand with the image name in
it. The step nobody exercised was the platform choosing the image itself, and for
those three it chose the shared base image, which contains none of their
binaries.

So there are now three checks, in increasing cost.

`TestEveryRuntimeImageHasADefault` reads the Dockerfiles in the repository and
fails when one of them names a runtime whose default image is still the shared
base. `TestEveryCommandExistsInItsImage` collects every `/usr/local/bin/agenthub-*`
the platform executes — the container's start command, the initialiser, and the
wrapper each execution backend calls — and fails when one of them is not copied
by that runtime's Dockerfile, following `FROM` so an image built on another
inherits what it ships. Both read the repository rather than a list somebody
maintains, and both run in CI.

`web/scripts/platform-path-e2e.mjs` walks the whole thing against a real cluster:
catalog template, agent, spawn, the operator's Pod, ready, session. It is not in
the default suite because each runtime it walks pulls a large image, and it found
a second bug on its first run — `/api/v1/runtimes` read the database directly
while only `/api/v1/agents` refreshed status from the cluster, so an operator
watching the Runtimes screen saw a Pod that had been running for minutes still
reported as pending.

## Runtime adapter contract

The Portal implements a common `Spawner` interface. Kubernetes is the default
implementation and creates an `AgentRuntime` resource. Runtime-specific launch
behavior lives in the offline `agenthub-base` image:

- `opencode`: `opencode serve --hostname 0.0.0.0 --port 4096`, authenticated with `OPENCODE_SERVER_PASSWORD`
- `hermes`: `hermes gateway run --no-supervise` with its authenticated API on 8642 and the loopback Dashboard/proxy pair on 9120/9119
- `qwenpaw`: `qwenpaw app --host 0.0.0.0 --port 8642`; the app ships no authenticator, so it is published only through the `agenthub-runtime-proxy` sidecar on 9119
- `qwencode`: `ttyd` serving the Qwen Code terminal on `127.0.0.1:7681`, published only through the proxy on 9119. Its own image too, `agenthub-qwencode`, versioned by `QWENCODE_VERSION`: it carries a Node toolchain and the agent itself, and it is deliberately leaner than the shared image — a coding agent needs git, grep and a language runtime, not the whole data-science toolchain.
- `langflow`: `langflow run` bound to `127.0.0.1:7860`, published only through the proxy on 9119. This one does **not** boot from `agenthub-base`: it is its own image, `agenthub-langflow`, versioned by `LANGFLOW_VERSION` and published as its own archive, because Langflow carries a Python tree and a built frontend that no other adapter needs and moves on its own schedule.
- `jupyter`: `jupyter lab` bound to `127.0.0.1:8888` under `--ServerApp.base_url=/{runtimeId}/`, with token authentication off because the proxy in front is the authenticator. Its image is the Qwen Code image plus the notebook toolchain, so the lab's own terminal has the agent on PATH and a task can drive that agent headlessly — the analyst and the coding agent share one workspace.
- `nodered`: `red.js` on `127.0.0.1:1880` with `httpAdminRoot` set to the runtime's own path, its user directory on the home volume rather than the image's root-owned `/data`.
- `n8n`: `n8n start` on `127.0.0.1:5678`, served at the root of its own origin. It has a base-path setting and rewrites its HTML to use it, but with that setting on its static assets and its REST API both fall through to the index page — the browser is handed HTML where it asked for JavaScript — so it is marked `HostSessionOnly` like Langflow.
- `custom`: an administrator-approved image and command

Runtime UIs are proxied verbatim: the session gateway authenticates, rewrites
redirects and strips AgentHub's own cookies, but never rewrites the response body.
Cookies are dropped by the `agenthub_` prefix rather than by name — the path
gateway names its access cookie per runtime — and everything else is forwarded in
both directions, because a runtime UI may keep a session of its own: Langflow
signs the browser in automatically and then authenticates its own API calls with
the cookies that response sets. A `Set-Cookie` coming back from a runtime is kept
for the same reason, minus two things: a name in the platform's own namespace,
and, under a path prefix where the Portal's origin is shared, a scope wider than
`/{runtimeId}`.

Two more rules exist because of where a runtime UI looks for things.

A runtime whose client addresses its own endpoints relative to a base path rather
than to the page's URL has to be served under that base path in both session
modes. `runtimetype.ServesUnderRuntimePath` marks those; the path gateway keeps
the prefix instead of stripping it, host mode opens them at `/{runtimeId}/` too,
and the adapter starts the program with the same prefix. Qwen Code is the case:
ttyd asks for `/ws` relative to its base path, so at the origin root behind a
prefix it asked the Portal for `/ws` — and a WebSocket handshake carries no
`Referer` for the path gateway to route by. The page loaded and the socket did
not, which reads as a terminal that renders and then offers to reconnect.

And a launch ticket never survives into the address bar. Both gateways redirect
after exchanging one, because a runtime UI builds its own URLs from the page's
location: ttyd copies the query string onto its websocket, which sent the spent
ticket back and was refused. A stale ticket arriving with a valid session is
treated as a stale URL rather than an intrusion.

A runtime that addresses its assets from the origin root and has no base-path
setting cannot be served from the Portal under `/{runtimeId}/` at all. Langflow is
one, so `runtimetype.HostSessionOnly` marks it and a launch without a Runtime Base
Domain is refused with that reason instead of opening a blank page.

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

Qwen Code reads `~/.qwen/settings.json` for its model and its MCP servers, and
`~/.qwen/.env` for the credential. The MCP entries are declared as `httpUrl`
rather than `url` — in this product the second one means SSE, and a streamable
HTTP server declared under it never connects, which looks exactly like a tool
policy that denied everything. Its initialiser also creates the agent's own
Python environment on the home volume with a `.pth` file pointing back at the
image's toolchain, because the default security profile mounts the root
filesystem read-only: without it `pip install` fails in a coding agent, and with
it packages land on a volume that survives the Pod and take precedence over the
preinstalled ones.

Langflow has no configuration file: it is configured entirely through the
environment. The platform sets where it listens, that automatic login is on, that
its API checks the runtime's own token as `x-api-key`, where its database and
generated secret key live (`LANGFLOW_CONFIG_DIR` on the home volume, so flows
survive a restart) and that telemetry is off. The model binding reaches flows as
global variables through `LANGFLOW_VARIABLES_TO_GET_FROM_ENVIRONMENT`, so a person
drawing a flow does not retype the endpoint and its key into every component.

Each adapter is also described for the people choosing one — what it is good at,
whether it has a terminal, where its files live, whether MCP servers reach it
through its own configuration, and whether its surface is published directly or
through the platform's proxy. `internal/runtimetype.Describe` is the one place
that says so, served at `GET /api/v1/runtime-types` and rendered by the console.
The console used to carry its own copy of the labels and one-line summaries, which
meant the comparison a person actually needs lived nowhere and the two copies had
already begun to drift. A test pins the described ports and proxy flags to the ones
the operator opens, because a description that disagrees with the deployment sends
somebody to a port nothing listens on.

## Runtime settings, injected and verified

The platform generates each runtime's own configuration: the model provider block,
the MCP servers, the terminal's working directory. Everything else a site needed —
its locale, its time zone, whatever option that product exposes — had nowhere to
go. Mounting a second copy of the same file fights the generated one, and a
platform-wide file cannot be per-runtime-type: `LANG` for one adapter's terminal
is not necessarily right for another's.

`internal/runtimecfg` holds one overlay per runtime type: a JSON object merged into
the generated configuration and a set of environment variables exported to every
container. Objects merge key by key so that setting one field leaves the rest of
its section alone; anything else replaces, because a site that writes a list means
that list. The keys the platform owns — `model`, `mcp`, `provider`, `mcp_servers`,
the Qwen Paw provider binding — are refused at the edge and skipped again at merge
time: an overlay that broke them would look like a platform fault, and the two
things a site is most likely to believe it configured correctly are exactly those.

Injection happens where each runtime's configuration is written, which is not the
same place for all three. OpenCode and Hermes read a file the operator renders, so
the merge is done there. Qwen Paw writes its own configuration during
initialisation, so its overlay is delivered as a patch its initialiser applies
after `qwenpaw init` has created the file — merging earlier would be overwritten by
the initialiser itself. Environment variables go to every container, and the
overlay's fingerprint is folded into the Pod template hash so that changing a
setting rolls the Pod: without that, a saved overlay would sit in a ConfigMap while
the running Pod kept its old one.

The part that makes it usable is the report. Every initialiser ends by reading back
the file it just wrote and posting to the control plane — authenticated with the
runtime's own token, the same way the in-Pod gateway asks for tool approvals —
which keys are in it, the fingerprint it applied, and whether the file was written
at all. Keys only: an overlay may carry an internal endpoint or a licence string,
and neither belongs in a status record. The delivery is best effort, because a
report that cannot be sent must not stop a runtime from starting; the missing
report is itself the signal.

That produces four states an operator can act on, and the distinctions are the
point. `applied` means a running Pod reported this exact settings version.
`stale` means it started before the change, so a restart will fix it. `unverified`
means nothing has been reported yet — not a failure, and calling it one would send
somebody debugging a Pod that is working as designed. `failed` means the Pod said
it could not write or read the file, with its own message.

The console offers a catalogue of settings sites usually want, and it is honest
about two different kinds of entry. A verified one names a key this platform
already writes or a variable the operating system defines — `LANG`, `TZ`,
`HTTPS_PROXY`, OpenCode's `autoupdate`, Hermes' `terminal.cwd`. An unverified one
describes a setting people ask for — an auto-approve mode, a theme, a skills path —
without naming a key, because those belong to the runtime's own version. The
platform will inject whatever key an administrator supplies and report whether it
landed in the file; it will not guess the key on their behalf. Guessing would
produce a configuration that looks applied and does nothing, which is the exact
failure this whole feature exists to remove. What the report proves is that the key
is in the file the runtime reads — whether that product honours it is the product's
business, and the console says so.

## Autonomous execution and the runtime

There are two ways an agent does work here and they are not the same thing. A
person opening the runtime gets the real product — opencode editing files, Hermes
running its own tool loop, a terminal. Autonomous execution is a prose loop
against the model gateway: the platform acquires the Pod so the workspace exists
and somebody can watch, but the loop itself cannot edit a file, run a command or
call a tool.

Nothing used to say so. The prompt described an autonomous agent and left its
limits unstated, and models filled the gap the way models do — reporting commits
they never made. The instruction now states the environment plainly: which runtime
this agent is bound to and what that runtime is, the workspace by name and whether
it survives the Pod, the MCP servers bound to the agent, and then the limit in one
sentence with no hedging.

The way out is a handover. `<<<HANDOFF summary ... detail ... >>>` parks the task
as `handoff` rather than failing it: the transcript stands, the attempt is not
counted as a failure, the owner is notified, and a person opens the same workspace
from the task row — the console starts the runtime if it is not running. They
finish the work and close the task with a note, which becomes its final word.
Only a handed-off task can be closed that way, and only as completed or cancelled:
letting anybody mark anything completed would make the status meaningless, and
leaving no way to close this one would keep every handover open forever.

A handover is offered only when it can happen — the agent has a persistent
workspace for the work to live in and the runtime has a surface a person can use.
Whether the task started the Pod is beside the point: a person can start it.

## Running the runtime's own agent

Some runtimes are a command line rather than a server. Qwen Code is one: it has a
tool loop, it runs in the workspace, and it has a headless mode meant for exactly
this. So for those, an autonomous task is that agent — executed in its own
container, with the Goal's guardrails handed over as the agent's own budgets:
max steps become `--max-session-turns`, max tool calls become `--max-tool-calls`,
and the duration becomes `--max-wall-time`, set slightly under the platform's own
deadline so the agent stops itself and says why instead of being cut off.

The Goal carries `runner: 'cli'` and one more setting that matters more than any
other: `cli_approval_mode`. `plan` changes nothing, `default` asks before every
change (which unattended means it stops), `auto-edit` and `auto` widen that, and
`yolo` approves everything. It defaults to `default` rather than to the
convenient one, and it is refused outright when the Goal also demands human
approval — an agent told to approve everything itself would sail straight past
the gate that exists to stop it.

Reaching the agent means executing a command in the Pod, which is why the control
plane's role has `pods/exec`. It grants nothing the role could not already do —
it creates the Pods and decides what they run — and a deployment that never uses
this runner can remove the rule; those tasks then fail with a permission error
rather than doing something unexpected. The command is argv, never a shell
string, and it goes through a wrapper the image ships (`agenthub-qwencode-run`)
because an exec has no working directory and no profile: the wrapper supplies
both, so a task title with a quote in it cannot become a command.

What comes back is the agent's own JSON: the result text, the turn count, the
tool calls, the lines added and removed, and — unlike a flow — real token usage,
so a CLI run is metered like any other work rather than described as unmetered.
Its exit codes are its contract: 53 for the turn limit, 55 for the time or
tool-call budget, 130 for an interrupt. None of those are retried, because the
same task with the same limits stops in the same place; anything else is treated
as a bad moment in the runtime and gets another attempt. On the guardrail exits
stdout is empty and the explanation is a JSON object on stderr, so both streams
are read — recording "no output" for a run that stopped for a reportable reason
would waste the person's time.

## Talking to the agent instead of parsing what it printed

The CLI runner works, and it works by knowing one agent. Its flags, the shape of
its JSON, the exit codes it uses to say which budget stopped it — none of that
transfers. The next terminal agent names its budget flags differently, reports
usage differently, and explains itself in its own words, so every agent added
that way is another parser to write and another set of exit codes to keep true.

The [Agent Client Protocol](https://agentclientprotocol.com/) is the industry's
answer to that: JSON-RPC over the agent's stdio, with `initialize` to negotiate,
`session/new` to open a session in a working directory, `session/prompt` to hand
over the task, `session/update` notifications while it works, and a stop reason
when the turn ends. A Goal with `runner: 'acp'` drives that conversation.

The reason to prefer it is not tidiness. It is `session/request_permission`: the
agent asks before it uses a tool, and the client answers. Under the CLI runner an
unattended task picks an approval mode up front and the agent then decides for
itself; here the platform is asked every time, and writes down what it answered.
A run that changed files ends with a record of which changes it was allowed to
make and which it was refused.

When the Goal asks for human approval, the platform does not answer at all: it
puts the question to a person and leaves the agent holding it. That is the same
machinery the in-Pod MCP gateway uses to hold a tool call open while somebody
decides, and an agent waiting on a JSON-RPC reply is exactly the shape it needs.
Anything that is not read-only is escalated, whatever the approval mode says —
reading is why the agent was started, and a Goal that wakes somebody to approve a
file read is a Goal nobody leaves switched on. The wait is bounded by the run's
own deadline rather than a deadline of its own: the time the Goal was given is
the time somebody has to answer, and a question nobody answers becomes a refusal
recorded as one.

This is why one combination is no longer refused. A Goal that wanted a person to
approve state-changing work could not previously also be permissive, because the
platform had no way to ask; under this backend it has one, so the two settings
compose — the person decides, and the mode decides everything the person is not
asked about. The headless runner still refuses that pair, because it hands the
mode to the agent and reads the result: there would be nothing left to stop it.

What the platform answers comes from the same `cli_approval_mode` the CLI runner
uses, because it is the same question and a second setting would only be a second
place to get it wrong. What differs is who enforces it. `plan` and `default`
allow only the protocol's read-only tool kinds — `read`, `search`, `fetch`,
`think` — and refuse everything else; `default` is strict on purpose, because
"ask before acting" with nobody at the keyboard honestly reads as no.
`auto-edit` adds `edit` and `move`, the workspace but not the world outside it.
`auto` and `yolo` allow everything, and like the CLI runner they are refused when
the Goal also demands human approval. A tool kind this platform has never heard
of is never allowed by anything but those two: an unknown verb is not a reason to
say yes.

Each tool call the agent makes and each permission answered lands on the run as
its own step, after the turn's own step, so the timeline reads in the order it
happened. The Goal's tool-call budget is enforced here rather than handed over,
because the protocol has no budget to hand: going over cancels the session, which
the protocol defines as ending the turn cleanly, and the failure says which limit
was hit rather than reporting a mysterious cancellation.

Token spend is metered when the agent reports it, and not otherwise. The
protocol has nowhere to put spend — its `usage_update` is context occupancy,
tokens in the window rather than tokens bought — so agents report the real
numbers in their own extension field, and Qwen Code does. When one arrives the
run is metered like any other work; when none does the run says so rather than
being credited with a number that is not one.

Two more things are deliberately not claimed. The agent's `agent_thought_chunk`
stream is counted but not stored: private reasoning is not the answer, and a
durable record is the wrong place for a model's scratch work. And no MCP servers
are passed to `session/new`, because the operator already wrote the agent's bound
servers into its settings pointed at the in-Pod policy gateway; handing the same
list to the session would give it two copies of every tool.

BrowserCode was the third, and it brought the browser — the one thing none of the
other runtimes can reach. It drives a real Chromium through the DevTools protocol
by writing JavaScript against the page rather than choosing from a fixed set of
actions, and it speaks the protocol without being asked to: unlike Qwen Code it
requests permission by default, and unlike Goose it labels a file edit an edit.

Its runtime runs two programs. Chromium listens for DevTools on loopback, started
by the runtime's own command; ttyd serves the agent's terminal. Three things
about that arrangement were found by running it, and each would otherwise have
shipped as a runtime that starts cleanly and fails on its first real task.
Chromium does not start inside an unprivileged container with its own sandbox on,
so it runs with that sandbox off and the Pod is the boundary instead — an
unprivileged user, no cluster credentials, and whatever network policy the site
applied. `--disable-dev-shm-usage` is not optional either, because a Pod's
/dev/shm is 64MB and a browser that runs out of it dies mid-page rather than at
startup. And the agent cannot find the browser by itself: its session looks for a
DevTools port file that current Chromium does not write when started with
`--user-data-dir`, so the image ships a note telling it to read the websocket URL
from `http://127.0.0.1:9222/json/version`, and the generated configuration points
the agent at that note. The readiness probe asks both the terminal and the
browser, because a runtime that came up with only the first is one whose tasks
all fail on their first tool call.

One thing about it is stated in the console rather than worked around. Its
browser tool — the reason to run this runtime — is announced with kind "other",
so the fine-grained approval modes refuse it and only `auto` and `yolo` let it
work. That is the same situation Goose is in, and the platform now says so as a
fact on the descriptor (`coarseToolKinds`) that the goal drawer warns from,
rather than as a list of runtime names kept in the console.

Which runtimes speak it is one field on the runtime descriptor: the argv that
starts that runtime's agent as a protocol peer. Qwen Code — and JupyterLab, which
is built on it — is started as `agenthub-qwencode-run --acp --approval-mode
default` through the same wrapper the headless runner uses, so the agent sees the
workspace and whatever the person installed in it. That last flag is not a
default but the point of the exercise: started without it this agent approves its
own tool calls and never asks, verified against the real binary, which wrote a
file without a word. It stays `default` whatever the Goal chose, because the
Goal's mode decides what the platform answers, not whether it is asked — a
permissive Goal still leaves a record of what it permitted.

Goose and BrowserCode are the proof that the line is all it takes. It is Block's open agent, it
speaks the protocol natively as `goose acp`, and adding it needed no execution
code at all — an image, a descriptor entry, and the backend above drove it.
Two differences it brought are worth knowing, because both were bugs or
behaviours nothing but a second real agent would have surfaced.

The first was the platform's. Goose identifies its JSON-RPC requests with a
string where Qwen Code uses a number, both of which the protocol allows. A client
that decoded ids as numbers discarded every frame carrying one as "not ours" — so
its permission request was never answered and the agent waited for a reply that
was never coming, until the task's deadline killed the run. Ids are now kept as
they arrived and echoed back byte for byte.

The second is Goose's, and it is documented rather than worked around: it
declares every tool call's kind as `other`, reads included. The platform judges
by the kind an agent declares, so `plan`, `default` and `auto-edit` refuse
everything a Goose session tries and only `auto` and `yolo` let it work. Guessing
the kind from a tool's title would be the platform inventing a fact the agent
declined to state, so the console and the runtime's own description say this
instead, and an unattended Goose Goal is expected to choose `auto` — with every
call it makes still recorded.

## Investigating, with the evidence kept

The backends above answer a question. This one answers it and hands back what it
looked at.

HolmesGPT is a CNCF sandbox project that investigates production incidents: it
runs its own agentic loop over live observability data — Prometheus, Grafana,
Loki, alerts, runbooks — and reports a root cause. What makes it worth a backend
of its own rather than a prompt is that it also reports every query it ran and
what each returned. An investigation whose evidence cannot be checked is an
opinion, and an opinion about why production broke is worth very little at three
in the morning.

So a Goal with `runner: 'investigate'` executes one investigation in the agent's
own Pod and splits what comes back. The conclusion becomes the run's answer,
judged by the same evaluator as any other. Every tool call behind it becomes a
step on the run's timeline — the query, the toolset it came from, and whether it
succeeded — so a person reading the run afterwards can see which query produced
the number the conclusion rests on, and that half of them did not fail. Token
usage is the agent's own count, so an investigation is metered like any other
work.

Reaching it is the same exec the headless runner uses, through a wrapper the
image ships. The wrapper exists for one reason worth stating: the agent renders
its answer for a person while it works and writes the machine-readable record
only to the file named by `--json-output-file`, so parsing its output would mean
hunting for JSON inside prose. The wrapper points that flag at a temporary file
and puts the file on stdout, leaving stdout carrying the record and nothing else.
It keeps stderr from the same run rather than repeating the investigation to find
out why one failed — a second investigation would spend a second investigation's
tokens.

Two limits are deliberate. Reading metrics and logs is what an investigation is;
running shell commands to find out more is a different kind of act, so the agent
is started with `--bash-always-deny` unless the Goal's approval mode is one of
the two that were chosen deliberately, and that combination is refused when the
Goal also demands human approval. And the Kubernetes toolset does not work here:
runtime Pods are given no service account token, on purpose, so the agent cannot
read the cluster it runs in. That is said in the runtime's own description rather
than left to be discovered, and a site that wants more points the investigator at
its Prometheus or Grafana through the runtime settings overlay — which is also
where its toolsets are configured.

The agent's own defaults would enable the Kubernetes toolsets and Robusta's cloud
integration; neither can work in a runtime Pod, and left on they fail their health
check every start and then tell the model they could not look. The operator starts
it with `internet` alone and lets the overlay add the rest.

## Calling an application the platform does not run

Some products are not one container. Dify is a dozen services — api, worker,
beat, web, a plugin daemon, sandboxes, a proxy, PostgreSQL, Redis and a vector
store — and reproducing that topology inside a Pod would mean carrying a fork of
somebody else's deployment and following it every release. What a site has is a
Dify they already run.

So `external_apps` holds an address, a credential and enough about the API to
call it correctly, administered like the model endpoints are. A Goal with
`runner: 'dify'` names one, and the task becomes a call: `POST /v1/workflows/run`
for a workflow app, `POST /v1/chat-messages` for a chat app, authenticated with
the app's own key. The kind is stored rather than guessed because the two answer
differently — one returns named outputs, the other an answer — and a workflow
that failed says so in its body with HTTP 200, so the status is read rather than
inferred from the code.

This is the only backend that starts nothing: no Pod, no runtime, and therefore
no dependence on the agent's runtime type. What the platform still keeps is
everything around the call — the content scanner on the way out and back, the run
record and its step, the artifacts and memories the answer declares, the
completion verdict, the quota and the audit trail.

Two things are deliberately not claimed. The tokens the app reports are recorded
as the app reported them and kept out of the platform's own metering, because
they were bought against that deployment's model provider rather than the
endpoint this platform holds. And a workflow's outputs are whatever its author
named them, so a single output is used as it is and several are kept together
with their names rather than one being picked and the rest dropped.

## Running a flow instead of reasoning

The prose loop above is a compromise the platform makes because it cannot reach
inside a runtime. A Langflow agent does not need it: the flow somebody drew in the
editor is the program, and the platform can execute it.

A Goal carries `runner`. `prose` is the loop described above and the default, so
every agent that already exists keeps behaving exactly as it did. `flow` runs the
Goal's `flow_id` in the agent's own runtime — one POST to
`/api/v1/run/{flow_id}` through the in-Pod proxy, with the runtime token as both
the proxy's Basic credential and Langflow's `x-api-key`. The task's title, input,
goal description and constraints become the flow's input value; the task id
becomes the Langflow session id, so a flow's own memory components see one
conversation across retries.

Everything around it stays the platform's: the run record and its step, the
artifacts and memories the answer declares, the completion verdict from the same
strategies, the quota, the audit trail. What changes is where the work happened.

Three things are deliberately not claimed. The platform does not meter a flow's
model calls — they happen inside the flow, against whatever endpoint its own
components point at — so `token_usage` is passed through as the runtime reported
it, in the run event, rather than turned into a billed number. The answer is read
from the several places Langflow reports it (`results.message.text`, then
`outputs.message.message`, then `artifacts.message`, then `messages[]`), because
which one is filled in depends on the component; a Goal that names an output
component gets that one. And an HTTP 4xx is not retried: an unknown flow id or a
rejected credential does not become valid on the next attempt, and spending the
retry budget on it only delays the report.

Content scanning happens before the runtime is addressed at all. `guard.NewFlow`
is the same inspector the model client uses with a different policy action
(`workflow.run`) and audit trail (`dlp.flow`): a task that must not leave the
platform has no business being handed to a flow engine, and the flow's answer is
inspected on the way back for the same reason a model's is.

Choosing a flow is picking from a list the runtime itself answers —
`GET /api/v1/agents/{id}/flows` asks Langflow with `header_flows=true`, which drops
each flow's graph and turns a five-megabyte answer into seventeen kilobytes. The
platform keeps no copy: a copy is what would go stale. When the runtime is
stopped, the id can still be typed in.

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

## Common runtime environment

Every offline site has settings that belong to all runtimes rather than to one
agent: the internal PyPI index in `/etc/pip.conf`, the npm registry, an HTTP
proxy. Administration ▸ System Settings ▸ Runtime Environment holds them once —
a list of files with their target paths and permissions, and a list of
environment variables — and every runtime is provisioned with the same set.

The path is short on purpose. The control plane validates the declaration, puts
it on the `AgentRuntime` object under `spec.provisioning`, and the operator
renders the files into a ConfigMap of their own (`<runtime>-files`) which every
container in the Pod mounts read-only through `subPath` at the declared path.
Variables are prepended to every container's environment, including the
adapters' init containers and the platform's sidecars: an init container that
installs packages needs the index as much as the agent does.

Three properties are worth knowing:

- **Not a secret store.** A ConfigMap and a CRD are both readable by anyone with
  RBAC on the namespace. Credentials belong in personal secrets or MCP
  credentials, which travel in the runtime Secret instead.
- **The platform's own paths and variables are refused.** `/etc/agenthub`,
  `/usr/local/bin`, an adapter's installation directory, `HOME`, `PATH`,
  `OPENAI_*`, `AGENTHUB_*` and the rest are rejected on save and dropped again
  by the operator. Otherwise this setting would be a way to redirect a model
  binding or replace a platform binary for every agent at once.
- **An edit rolls the Pod.** A `subPath` mount never sees a ConfigMap update, so
  the provisioned set is part of the Pod template's configuration hash. Changing
  a file therefore replaces the Pod rather than silently doing nothing.

Saving the setting used to change nothing that was already running. Each runtime's
AgentRuntime object carries a copy of the files and variables — that copy is what
the operator reads — so an administrator who added `/etc/pip.conf` on an offline
site watched their agents keep failing to install anything, with no way to tell
that the setting had simply not reached them. Saving now rewrites the object of
every runtime that has one, keeping each runtime's desired state exactly as it was
(a push must not start what somebody stopped), and the response says how many
runtimes it reached. The operator folds the files into the Pod template hash, so a
Pod whose content actually changed rolls and one whose content did not is left
alone.

One failure mode deserved its own error. A CRD prunes fields its schema does not
declare, silently: a cluster still running an older `AgentRuntime` definition
accepts the write and drops the whole provisioning section, which looks exactly
like the feature not working. The write reads back what the API server stored — no
extra request, since an update returns the object — and reports it as what it is,
naming the manifest to re-apply.

## Agent toolchain in the base image

The runtime image carries a toolchain, because an agent asked to write code has
to be able to run it. `python` and `pip` come from a virtualenv at
`/opt/agenthub/venv` that leads `PATH`, with the libraries a coding agent
reaches for first — ruff, black, mypy, pytest, ipython, httpx, pydantic, numpy,
pandas, matplotlib, beautifulsoup4, openai, anthropic and mcp among them. Node
carries typescript, tsx, prettier, eslint and pnpm
alongside OpenCode, and the image also has build-essential, ripgrep, jq and
sqlite3.

`conda` and `mamba` come from Miniforge in `/opt/conda`, configured for
conda-forge only, with `envs_dirs` and `pkgs_dirs` pointed at `/home/agent` so
an environment an agent creates survives the Pod that created it. pip, uv and
npm caches are pointed at the same volume for the same reason.

The toolchain is a virtualenv rather than the system interpreter so that an agent
installing a package cannot break the adapters that share the image. It is
owned by the runtime user, so `pip install` works directly when a security
profile leaves the root filesystem writable; under the default read-only root
filesystem an agent creates its own environment under `$HOME` or `/workspace`,
both of which are persistent volumes. Debian's `/etc/profile` rewrites `PATH`
for login shells, so `/etc/profile.d/agenthub-toolchain.sh` puts the toolchain
back and sources conda's shell hook — `conda activate` is a shell function and
does not exist without it.

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
was in flight.

Delivery is attempted under a lease and marked done only once it finished. The
dispatcher used to mark a batch delivered in the same statement that claimed it,
which made the outbox durable against a restart but not against anything going
wrong afterwards: a worker that died, or a task insert that failed, left the event
recorded as delivered with nothing delivered. Now a claim takes a lease and counts
the attempt, a failure reschedules the event with a growing backoff, and an event
nobody could deliver is dead-lettered after five attempts — kept, with the reason
on it, and its owner told, because an event that never arrived is otherwise
indistinguishable from one nothing subscribed to.

Each subscriber's task is created together with its ledger row in one
transaction, and the ledger's primary key is (event, subscriber). That is what
makes redelivery safe: a worker that died between creating the task and marking
the event delivered leaves a ledger row behind, so the next attempt skips that
subscriber instead of queueing the same work twice. The result is one delivery per
subscriber rather than at least one, and `deliveries`/`deliveredTo` on the event
feed answer the question a ledger exists for — did this event actually reach that
agent?

Two things keep event triggers from becoming a feedback loop. A subscription
carries an optional payload filter, applied as jsonb containment in SQL, so an
agent watching one runtime is not woken by every other runtime's failures. And
every event records the trigger that caused the work it reports on; a trigger
never fires on an event its own task produced, which is what stops an agent from
waking itself forever. Publishing is best-effort throughout: the task already
finished, and failing it because nothing could be told about it would be worse
than a missed trigger.

## Decisions the platform acts on

Two answers are not passed along to a person or another agent — they are acted on
by the platform — and both used to be read out of prose.

The router's choice was found by looking for a branch's step id or its agent's
name anywhere in the answer. A router that wrote "이 건은 배포팀에 보내지
않습니다" selected 배포팀, an answer that mentioned two teams selected both, and
anything in the input that named a branch could steer the graph. The router is now
told which ids exist and answers with a list of them, constrained by a JSON schema
whose `branches` items are an enum of those ids. What comes back is validated
against the same list — a gateway that ignores `response_format` accepts the
request all the same, so the answer is never taken on trust — the chosen branch
receives a `handoff` message rather than the decision JSON, and the decision itself
is kept on the run so it can be read afterwards instead of inferred from which steps
happened to run.

The completion verdict has the same shape: the judge is asked for
`{passed, reason, unmet}` under a schema whose `unmet` items are an enum of the
criteria that actually exist, so a judge cannot fail a task against a requirement
nobody wrote. Anything it names that was not configured is dropped from the verdict
and said out loud in the reason rather than stored as a criterion.

Both fall back rather than fail. Not every OpenAI-compatible gateway implements
`response_format`, and an offline site cannot choose its gateway freely, so a 4xx
refusal of the schema means the same prompt is sent again without it and the answer
is marked unvalidated — the caller parses and validates either way. What differs is
what happens when the answer still cannot be read: a routing decision that cannot
be read runs the whole graph and says so on the record, because an empty result is
worse than an unrouted one; a verdict that cannot be read fails the task, because a
completion nobody could confirm is not a completion.

## Consensus

Consensus was selectable in the console long before it meant anything: the run
followed the graph like any other chain and returned the last agent's answer.
It now does what the label promises.

Edges are ignored for this mode. An agent that has already read another's answer
is not casting an independent vote, and workflows saved as chains before the mode
worked still have to behave as a consensus, so every participant is asked the
original question alone. Each is told to end with a `VOTE:` line, and the engine
counts those lines itself rather than asking a model to summarise the room —
the tally is then reproducible and an operator can check the arithmetic.

Votes are compared after normalising case, spacing and punctuation, because two
agents that agree rarely type it identically; a similarity score would instead
make the verdict depend on how it was tuned. An answer with no vote is recorded
as an abstention rather than guessed at, and a tie is reported as a tie —
presenting the first of two equal answers as the winner would hide exactly the
disagreement the mode exists to surface. The full tally, including every
dissenting position, is stored on the run so the decision can be defended later.

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

## Content scanning

An agent is a program that reads whatever it is pointed at and then sends it
somewhere else: to a model gateway, to an MCP server, to a tool that writes a
ticket. On an offline site that is exactly the risk — the data never had to leave
the building until an agent helpfully summarised it into a prompt.

`internal/dlp` holds the detectors and nothing else: no database, no HTTP, no
settings loader, so the in-Pod sidecar can use the same code without linking a
PostgreSQL driver into a sidecar. They are deliberately conservative. Anything
with a check digit is checked against it — a resident registration number that
fails its checksum is not one, a card number that fails Luhn is not one, and
thirteen digits in a log line should not stop a production run. A scanner that
cries wolf is switched off within a week, which is the failure mode that matters
more than a missed pattern.

Each class carries an action: off, audit, redact, block. Redact is the useful
default for model calls — the agent keeps its context and the number never leaves
— and block is for tools that write to another system, where a partial send is
worse than no send. A class nobody configured is not scanned, so adding a detector
to the platform never starts blocking somebody's traffic without them choosing it.

There are two enforcement points and they are different for a reason. Model calls
all pass through one client, so the check hangs off that: an inspector on
`ModelCompletion` sees every prompt and, optionally, every answer. Tool calls
never pass through the control plane at all — the agent talks to the in-Pod
gateway and the gateway talks to the MCP server — so scanning there is the only
place a customer record on its way into a ticket can be caught, and the settings
travel to the Pod the same way the tool policy does. When the gateway redacts, it
rewrites the JSON-RPC body rather than re-marshalling a parsed copy, so fields the
gateway does not understand survive the trip; when it cannot rewrite the body, it
refuses rather than sending it unchanged.

The scanner says what is there and the policy says what this deployment does about
it: findings become the `dataClasses` of a policy request, so a rule can be
narrower than the global action — one agent, one role, one server. A refusal is
wrapped in a sentinel error so the execution plane fails the task instead of
retrying it, because the same prompt carries the same data and retrying would
spend the whole budget arriving at the same answer.

What is recorded is the class, the count and a masked sample — never the value.
A DLP trail that quotes what it found has moved the problem rather than solved it.
The in-Pod gateway reports its findings to the control plane over the same
authenticated channel it uses to ask for approvals, because scanning whose results
stay in a Pod log is scanning nobody can be shown.

## Policy as code

The controls were all real and all separate. An agent's MCP tools were an
allow/deny list on that agent, owned by whoever owned the agent. High-risk
approval was a global switch. Who could run what was ownership plus a role. Spend
was a quota. Every one of them was configured on a different screen, and none of
them could express the sentence a security review actually asks for — "this team
may not call anything that writes" — because there was nowhere to write it down,
and nowhere to check afterwards that it had been applied.

`internal/policy` is one document: an ordered list of rules, each with an effect
(allow, deny, require approval), the actions it decides, and selectors for who is
acting (role, user), what is being acted on (agent, MCP server, tool) and what the
request carries (data classes, which the content scanner fills in). Every selector
left empty matches everything; a rule matches when all of its non-empty selectors
match. Matching is exact and case-insensitive with one wildcard, a trailing `*`,
because a policy language nobody can predict is a policy nobody trusts.

Rules are evaluated in order and the first match decides, like a firewall. That is
not the most expressive arrangement — deny-overrides would be safer to reason
about in the abstract — but it is the one an operator can read top to bottom and
predict, and it makes a narrow exception above a broad denial the obvious thing it
looks like. The decision carries the rule that made it and every rule that would
have matched, so "why did my new rule do nothing" is answerable without bisecting
the document, and the console's simulator answers it against unsaved edits.

Enforcement happens at three kinds of point. Task creation and runtime start are
decided in the API, where the refusal carries the rule's own reason — a denial
nobody can act on is a support ticket. Tool calls are decided in the Pod: the
in-Pod egress gateway is the only place an agent cannot route around, so the
rules for one agent and one server are compiled into its configuration when the
runtime is provisioned. They compile to patterns rather than tool names, because
the tool list is not known at provisioning time and a policy that covers only the
tools we had heard of is not a policy. Both ends match with the same function
from the same package, so they cannot drift. A tool the platform forbids is also
stripped from the advertised list, and the refusal says the platform refused
rather than the agent's own settings — different sentences with different
follow-ups.

Saving the policy pushes it to running runtimes the same way the runtime
environment does, and every refusal is audited with the rule that caused it: a
policy nobody can prove was applied is one that gets argued about after an
incident rather than before one.

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

## Tool approval at the call

Approval used to be something the agent volunteered. Its goal asked it to declare
a state-changing action and wait, and the platform parked the task when it did —
which works right up until an agent simply calls the tool. The "approval required"
flag on an MCP server in the admin catalogue, and the high-risk-tool-approval
governance switch, enforced nothing at all.

The gate now sits in front of the call, in the in-Pod MCP gateway: the one place
the agent process cannot route around, since its generated configuration points at
the gateway and only the gateway knows the upstream address. On a gated
`tools/call` the gateway holds the JSON-RPC request open, asks the control plane to
create an approval, waits for a person, and then either forwards the call or
answers with a refusal the model can read. What is gated comes from three places:
the tools an agent's policy lists as needing approval, a server the catalogue marks
approval-required, and — while the governance switch is on — every tool of a
high-risk server.

The properties that make it a gate rather than a suggestion:

- **It fails closed.** A control plane that cannot be reached, a wait that runs
  out, or a runtime configured to need approval with no way to ask for one all end
  in a refusal, never in the call going through.
- **The deny list still decides first.** A blocked tool is refused without
  bothering a reviewer, and the API refuses to store a policy that gates a tool it
  also blocks.
- **The reviewer sees the call.** The approval carries the server, the tool and
  the arguments — trimmed, because a tool argument can be a whole file — so it can
  be judged on what it would do rather than on its name.
- **The gateway authenticates as its runtime.** It presents the runtime token from
  the Pod's Secret; the control plane stores only that token's hash and derives the
  agent, owner and reviewer from the runtime it identifies. A request may not name
  a different runtime, and one runtime cannot poll another's decisions.
- **Nobody has to be watching.** Creating the approval notifies the owner and
  places it in 검토 · 승인 next to every other pending decision.

## Route catalog

An endpoint used to be described in three places that had no way of knowing about
each other. chi registered the path. A middleware decided which API-key scope it
needed by looking for substrings in the URL. A hand-written list produced the
published OpenAPI description, and had drifted to about a fifth of the real
surface.

The middleware's guess was the worst of the three, because it was wrong in ways
nobody could see. `GET /api/v1/sessions` demanded `runtime:manage` — the scope for
starting runtimes — because its path contains "session", so a read-only key could
not list the sessions it was allowed to read. Any new write inherited
`agent:write` whether or not that was the right authority for it, and the only
way to find out what a key could reach was to try.

Writing implies reading. A key holding `agent:write` or `runtime:manage`
satisfies a route that requires `api:read`, because a key that can create an agent
but cannot list agents is not a smaller permission — it is an unusable one, and
the only way to discover the gap was to issue the key and watch it fail. Nothing
widens in the other direction: a read key still cannot write, an MCP key reaches
no REST route at all, and no key of any kind reaches a browser-only route.

`internal/api/catalog.go` now holds one entry per endpoint: method, pattern,
scope, role, tag and summary. The router is built from it, the API-key check is
the scope written on the line rather than one inferred from spelling, and the
OpenAPI document is generated from the same entries — 118 operations, tagged, with
path parameters, describing exactly what is served. Authorisation is applied per
route rather than as middleware over a group, because a middleware only sees the
URL, which is what produced the sessions bug.

Three tests hold it together. One walks the real router and fails on any route
that is not in the catalog, except a short list of unauthenticated endpoints —
login, the OIDC redirect, the trigger webhook, the in-Pod approval gateway — each
carrying its reason in the test. One fails on a catalog entry that reaches
nothing. One fails on a GET that requires a write scope, an /admin route that is
not administrator-only, or a credential route an API key could reach. Adding an
endpoint without deciding its permission is no longer possible; it does not
compile into a served route, and if it is registered elsewhere the walk fails.

Under `/api/v1` an unknown path now answers a JSON 404 instead of falling through
to the single-page app, which used to reply 200 with a document that a client
could only report as "the response was not JSON".

## Agent versions and promotion

A definition's version counter went up on every save and nothing kept the
definition it counted. Saving was the whole release process: the next scheduled
run executed whatever the definition said at that moment, evaluated or not, and
the only way back from a bad edit was to remember what it used to say.

Every save now writes an `agent_versions` row — create, update and YAML import
alike — holding the name, description, instruction, spec and the runtime, model,
MCP, workspace, security and network bindings as they stood. The snapshot is best
effort on purpose: the definition is already committed by then, and failing the
caller's edit because its history could not be written would trade the working
change for the record of it.

An evaluation is recorded against the version it judged (`agent_evaluations
.agent_version`). Without that column a passing result would keep vouching for an
agent that has since been rewritten, which is the one thing a promotion gate
exists to prevent.

One version can be *promoted*: `promoted_version` on the definition, with who
promoted it, when, and why. A promotion requires an evaluation that passed against
that exact version. Administrators may override that, but not silently — an
override needs a written reason and is stored as `검증 생략 승격: …`, visible in
the console and in the audit log.

The gate itself, `require_promotion`, is off by default, so an agent that never
asked for one behaves exactly as it did before this existed. When it is on and the
live version is not the promoted one, the execution plane refuses: the API returns
409 at enqueue so the person who asked hears it immediately, and the worker holds
any task already queued in a `blocked` state with a message naming both versions.

Held is not failed. A nightly run that arrives while an unreviewed edit is live
used to fail and have to be recreated by hand after somebody promoted, and the
failure looked identical to an agent that could not do its job. A blocked task
keeps its attempt budget, holds no worker slot, and is put back on the queue the
moment the version is promoted or an older one is restored — the promotion
response says how many tasks it just released. It refuses rather
than quietly running the promoted definition instead, because the Pod, its tools
and its workspace are all provisioned from the live definition — serving an older
instruction against a newer Pod would produce a run nobody could reason about. A
gate that cannot be read does not stop the work, for the same reason a quota that
cannot be read does not.

Restoring writes an old definition back as a *new* version rather than rewinding
the counter, so a run already recorded against v4 keeps meaning what it meant.
Restoring the version that was promoted re-promotes it on the spot: it is the
definition production was already approved to run, and asking for a fresh
evaluation would leave the broken version live while somebody waited for one.

## Administration

Every figure an operator needs was already in the database, and none of it was
readable as a whole: the usage report showed the caller's own agents, the queue
depth showed the caller's own tasks, and the audit trail showed the last hundred
rows in the order they happened. "Is this deployment healthy, who is spending
what, and what is stuck" took five screens and mental arithmetic, which is not
something anyone does at 2am.

`GET /api/v1/admin/overview?days=N` answers it in one response, aggregated over
the tables the execution plane already writes — no new meter to keep in sync.
It carries accounts (total, signed in during the window, roles, never used),
agents and runtimes (running, warm, autonomous, gated, and how many gated ones
are sitting on an unpromoted definition), execution health (tasks by status, runs,
retries, success rate, median and p95 run duration), the queue with the age of its
oldest runnable task, the event outbox (pending, retrying, dead-lettered, and how
long the oldest undelivered event has waited), and spend broken down by user, by
agent and by model with a daily series. Totals are computed over every row rather
than summed from the truncated breakdowns, which would understate the bill by
exactly the tail.

The console derives an attention list from those numbers rather than printing
them and leaving the reader to spot the wrong one: tasks held by a gate, tasks out
of retries, a queue with no worker behind it, undelivered events, failed runtimes,
tokens spent on an endpoint with no price. Each links to the screen that acts on
it.

The audit trail is searched rather than scrolled — actor (partial,
case-insensitive), action (by prefix, so `agent.` selects a family), resource,
outcome and a time window, paginated with the total beside it. Every value is a
bound parameter: the trail is the one table where an injection would be least
likely to be noticed. Both the spend breakdown and the filtered trail download as
CSV, with a UTF-8 BOM, because Excel on a Korean Windows install reads a BOM-less
file as the legacy code page and shows mojibake, which reads as a broken export
rather than a spreadsheet setting. An export is itself an administrative action
and appears in the trail.

The user list carries the same aggregates per account — agents owned, tasks and
failures in the window, tokens spent — so "what is this account for" and "is it
still used" are answered where the account is managed.

## Operating the execution plane

Three things had no answer outside the database. A queue with no worker behind it
looked exactly like a quiet queue, because "how many workers are there" was
inferred from whoever happened to be holding a task. A task claimed by a worker
that then died stayed at `running` forever — the claim carried a lease, and
nothing ever reaped one, so the row sat where the claim query (which looks only at
`queued` and `retrying`) would never see it again. And nothing ever removed
finished history, so the largest tables grew until somebody noticed the disk.

Workers now register themselves in `execution_workers` and heartbeat every ten
seconds; forty-five seconds of silence marks one stale. A clean shutdown records
itself as stopped, which is what separates a deployment from a crash in the list
an operator reads. `LiveWorkers` is what the overview counts, so "nothing is
happening" and "nothing can happen" are finally different sentences.

A caretaker runs on every worker. Every half minute it returns tasks whose lease
expired more than a minute ago to the queue — with the attempt already counted,
because the attempt did happen, and pretending otherwise would let a task that
reliably kills its worker loop forever — and closes the run that was in flight so
the history has no run that never ended. Every hour it trims history past the
configured retention. All of it is idempotent SQL, so several caretakers doing the
same sweep need no coordination.

Execution can be paused. Workers finish what they are running and claim nothing
new; queueing continues, because a pause is for an upgrade or an incident and
losing the work that arrived during one would be the worse outcome. The switch is
read once per poll with a five-second cache, and a switch that cannot be read
means "running": inferring a pause from a query error would stop every deployment
whose database hiccuped. The pause reaches `/capabilities`, so the people whose
work stopped moving — who are not administrators — see the reason rather than a
queue that went quiet.

The rest is recovery an operator can reach: reclaim now rather than waiting for
the caretaker, requeue dead-lettered or failed tasks in bulk (bounded, and only
those two states — a completed task would run twice and a cancelled one was
stopped on purpose), redeliver events the outbox gave up on (the delivery ledger
means subscribers that already received them do not get them twice), and sweep
history with a dry run first. Retention has floors — thirty days for the audit
trail, seven for runs and tasks, three for events — because a mistyped number
deletes what cannot be reconstructed.

## Execution quotas

Runtimes, CPU, memory and storage were bounded per user. What a user could *run*
was not: one person could hold every worker slot, and an agent that never
converges could spend a month of tokens overnight, because the only spend signal
was a report somebody had to open.

Two limits now apply to the execution plane, both configured in Administration ▸
System Settings ▸ Governance and both off when zero. `maxRunningTasksPerUser`
bounds how many of one user's tasks execute at once across every agent they own.
`tokenBudgetPerUser` and `costBudgetPerUser` bound spend over the same 30-day
window the usage report shows, and an agent can carry its own `tokenBudget` so one
runaway agent stops without stopping everything else its owner runs.

The two kinds of limit end differently, because they clear differently. A
concurrency limit clears when somebody else's task finishes, so the task goes back
on the queue and does not spend a retry attempt — waiting is not a failed attempt,
and counting it would let a busy hour exhaust a task's budget before it ever ran.
A spent token or cost budget does not clear for days, so the task fails with the
numbers in the message and its owner is notified, rather than holding a worker
slot until the window rolls over. A budget that is already spent is also refused
at enqueue time with 429, so the person who asked hears it while they are still
looking; concurrency is never refused there, because queueing is what a queue is
for.

The decision is made after the claim and under a per-owner advisory lock, and a
task that stands down does so in the same transaction it was counted in. Counting
alone is not enough: two tasks claimed in the same instant each see the other
running, so with a limit of one both would step aside and neither would run —
which is exactly what happened the first time this was tried against a live
worker. Serialising the decision per owner makes as many tasks run as the limit
allows, no more and no fewer.

A quota check that cannot be run does not stop the work. The task already survived
a claim against the same database, and turning a transient query error into a
platform-wide stop would make a spend limit an outage.

## Tracing

The platform carried a trace id from the request that started a task through to
every step's log line, which answers "what happened in this run" for whoever is
reading logs. It does not answer what an operator asks when a nightly agent got
slow or expensive: where the time went, which model call cost what, whether the
runtime acquisition or the gateway was the bottleneck.

With an OTLP collector configured in Administration ▸ System Settings ▸
Observability, the API and the worker export OpenTelemetry spans. One
`task.execute` span per attempt carries the task, agent, attempt, priority and
source, and closes with the run id, status, step count, resumed steps, total
tokens and model; each reasoning step is an `agent.step` child with its own token
counts; the completion judge is a `task.evaluate` child; acquiring a runtime is
`runtime.acquire`; the workflow engine produces `workflow.run` with a
`workflow.step` per node. The API records one span per request, named by chi's
route pattern so spans group by route rather than by id.

Two details make the traces usable rather than decorative. The platform's own
trace id becomes the OpenTelemetry trace id whenever a span is recording, so the
id shown in the console, printed in the access log and stored on the run is the
string that finds the trace in the collector — with tracing off it falls back to
the previous `task-<id>-<attempt>` scheme. And a caller's trace context is
adopted: a request that arrives with a `traceparent` continues that trace instead
of starting a second one.

Tracing is off unless an endpoint is set, and off means free: no exporter is
installed, the global tracer is the SDK's no-op, and every span call in the
codebase becomes a couple of function calls with nothing recorded or buffered. An
offline site with no collector pays nothing for the instrumentation being there.
A collector that is misconfigured or unreachable is a reason to run without
traces, never a reason for a process not to start, so `Install` logs and carries
on. The setting is read at startup: changing it applies when the API and the
worker restart.

## Token spend

Agents in the execution plane run when nobody is watching, which is exactly when
a loop that fails to converge costs money quietly. Spend is therefore reported
from what the runs already recorded — every step stores its prompt and
completion tokens — rather than from a second meter that would drift from
reality the first time one of them was missed.

Input and output are priced separately because every provider charges them
separately, and an agent that reads a large workspace to write a short summary
has a very different bill from one that does the reverse. Prices live on the
model endpoint, per million tokens, in whatever currency the site uses: an
offline deployment has no rate to convert with, so the currency is a label on
the number rather than a conversion.

An endpoint with no price is reported as tokens and never as money. Pricing it
at zero would produce a confident total that understates the bill, so those
tokens are counted separately and the console says they are unpriced. A report
is scoped to the caller's own agents unless an admin asks for the whole
platform, and the window is bounded, because a year of steps is a table scan
nobody is waiting for.

## GitOps definitions

An agent definition is configuration, and configuration that exists only in a
database cannot be reviewed, diffed or promoted from a staging cluster to a
production one. A definition therefore exports as a YAML document and imports
back from one.

References travel as names, not identifiers. An id from one installation means
nothing in another — the profiles, workspaces and endpoints there have their
own — so the document names what it wants and the import resolves it locally.
A name the target cluster does not have is reported by name and the import is
refused, because an agent that comes up without its workspace or its model looks
imported and is not.

Re-importing the same document updates the agent rather than creating a second
one, which is what makes a GitOps flow possible: the file is re-applied on every
change. A renamed document creates a new agent, which is how a definition is
promoted into a cluster that has never seen it. The runtime type stays immutable
even when a document says otherwise, for the same reason it is immutable in the
console: the Pod, its ports and its persisted home are all shaped by the adapter.

What does not travel is anything installation-specific: owner, version,
timestamps, and every secret. A credential is referenced by the binding that
holds it, never exported into a file someone is about to commit.

## Checkpoint retry

A retry used to be a restart. The task went back to step one with an empty
transcript, so every reasoning step was paid for a second time and any step that
had already changed something outside the platform — a deployment, a delegated
task, a file written in the workspace — happened again. The same held for a task
that resumed after an approval: the reasoning that led the agent to ask for
approval was discarded before the decision was applied.

An attempt now starts from what earlier attempts finished. Before the run is
created, the orchestrator reads the task's completed reasoning and delegation
steps, seeds the transcript with them, and continues the step numbering, so one
task reads as one piece of work rather than several that all start at 1. The run
records how many steps it inherited (`resumedSteps`) and the timeline says so, so
a resumed attempt never presents inherited work as its own.

Four things keep this honest:

- **Scoped to one agent version.** A definition that changed since — a new system
  prompt, a different goal — invalidates the reasoning done under the old one, so
  the checkpoint is read for the current version only and anything older is
  ignored rather than resumed into.
- **Delegation results are steps.** They used to live only in the in-memory
  transcript, which meant a resumed attempt could hand the same work to another
  agent twice. They are recorded as steps of their own now.
- **Bounded.** A long history would resume into a prompt that costs more than
  redoing the work, so the newest 40 entries within a 40 000-character budget are
  carried and the model is told how many earlier steps were left out. The newest
  entry is always carried, however long: resuming with nothing would repeat it.
- **A restart is still available.** `POST /tasks/{id}/retry` with
  `{"fresh": true}` stamps the task's checkpoint horizon with the current time,
  which retires the earlier steps without deleting the record of them and lets
  that fresh attempt's own steps be resumed later. It is the right choice when
  the earlier reasoning rested on something that has since been corrected. An
  agent whose steps must always run from the beginning turns resuming off in its
  goal.

## Runtime warm pool

A scheduled task otherwise pays for a cold Pod. The image is already local, but
the volume, the adapter's init containers and the agent's own startup still take
most of a minute, so a trigger that fires at 08:00 does not start working at
08:00. The pool starts the runtime inside the agent's warm-up window and holds it
briefly after a task, so a burst pays that cost once rather than per task.

The pool is per agent, not a set of interchangeable Pods. A runtime carries its
agent's workspace, configuration and secret, all bound when the Pod is created,
so a generic warm Pod could not become this agent's runtime without a restart —
the very cost the pool exists to avoid. Both windows are bounded, because a
warm-up longer than the gap between fires is a runtime that is simply never off.

Ownership is explicit. The pool records a claim on the runtime row and only ever
stops runtimes it is holding; a person starting, restarting or opening the
workspace drops that claim, so the pool can never stop something somebody is
working in. Claiming happens before starting, so two workers warming the same
agent cannot start the same Pod twice, and a runtime with work still queued is
never cooled.

## Worker auto scaling

A worker ran a fixed number of tasks at once, which is wrong in both directions:
sized for the burst it holds model connections and memory all night, sized for
the quiet hours it drains a morning backlog two tasks at a time.

The limit now follows the queue between a floor and a ceiling the operator still
sets; equal values keep the fixed behaviour a deployment may be tuned for. Going
up is immediate, because a backlog is already costing someone time. Coming down
waits for several consecutive quiet passes, since a queue that empties for one
tick is usually about to refill and churning the limit churns runtimes with it.

Scaling down never interrupts work. The limit is expressed as slot tokens the
scaler holds out of circulation, so a reduction can only take tokens that are
free and lands on the rest as tasks finish. The depth the workers scale on is the
same one the console reports, so the two cannot disagree about how backed up the
plane is.

## Supervised workflows

Supervisor mode ran the graph like any other and concatenated the terminal step's
answer, which made the supervisor a last speaker: it could describe a problem
with a specialist's work but had no way to have it fixed, and nothing recorded
whether it had approved anything.

The supervisor now reviews. It sees the specialists' answers and either approves
or names the ones that need another pass, with what to change; only those
specialists run again, each seeing the feedback aimed at it, and the supervisor
reviews the new answers. The revised answer replaces the rejected one in the
trace, and the review record — who supervised, what was asked, whether it was
approved — is kept on the run.

Rounds are bounded, because a supervisor and a specialist that disagree will
disagree indefinitely and an unbounded loop spends a model budget discovering it;
the run then ends marked unapproved rather than pretending. The loop is counted
against the same call budget as the first pass. Approval is read only from a line
that is the word on its own — "이대로는 APPROVE 할 수 없습니다" is a refusal — and a
graph without one single terminal is left unsupervised rather than having a
reviewer promoted that the operator never nominated.
