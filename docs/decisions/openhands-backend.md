# OpenHands as an execution backend

Verified 2026-08-23 against `openhands-agent-server` 1.43.1, installed from PyPI
and driven over its REST API. Everything below was observed, not read.

## Why the server and not the canvas

OpenHands' own architecture already separates them: Agent Canvas is a UI, and the
Agent Server is "a REST API for running multiple agents on a single machine".
AgentHub is the control plane, so taking the canvas would be taking a second one.
What it wants is the server.

## The gateway is enforced by the request

Three agents in a row have now had a different answer to "how does this talk to
our gateway", and this is the best of them. Codex needs a file written into its
image; Pi needs a file written into its image; OpenHands takes it as a field:

```json
POST /api/conversations
{"workspace": {...}, "initial_message": {...},
 "agent": {"llm": {"model": "openai/<model>", "base_url": "http://gateway/v1",
                   "api_key": "<platform-issued>", "usage_id": "agenthub"}}}
→ 201
```

and the gateway's request log then shows:

```
/v1/chat/completions  auth=Bearer agenthub-issued-key
/v1/chat/completions  auth=Bearer agenthub-issued-key
```

Nothing has to be written into an image, and nothing has to be trusted to be
read: the endpoint travels with the work. That also means one server can serve
several deployments' models without being reconfigured.

## The REST surface

106 endpoints. The ones this integration needs:

| What | Endpoint |
| --- | --- |
| start work | `POST /api/conversations` |
| its timeline | `GET /api/conversations/{id}/events` |
| say something more | `POST /api/conversations/{id}/ask_agent` |
| the answer | `GET /api/conversations/{id}/agent_final_response` |
| branch it | `POST /api/conversations/{id}/fork` |
| compress context | `POST /api/conversations/{id}/condense` |
| approval gate | `.../confirmation_policy`, `.../events/respond_to_confirmation` |

`confirmation_policy` is worth noting: the server has its own approval gate, and
AgentHub already has one. Whichever is used, only one of them should be the
authority, and it has to be this platform's.

## Installing it is not what the metadata says

Three obstacles, all met on the way, all worth knowing before somebody tries:

1. **pip cannot resolve it.** `openhands-sdk` depends on `lmnr`, which pins
   `opentelemetry-semantic-conventions==0.60b1` against the `0.63b0`–`0.65b0`
   that `opentelemetry-instrumentation` wants: `ResolutionImpossible`. `uv`
   resolves it, which is what the vendor's own instructions use.
2. **`libtmux` is imported and not declared.** `api.py` imports it at module
   scope; without it the server exits before binding. `tmux` itself is needed too.
3. **`openhands-tools` is a separate package.** The server imports
   `openhands.tools` and the agent-server distribution does not bring it.

Pinning the SDK to the server's own version matters as well — pip installed
`openhands-sdk` 1.17.0 next to `openhands-agent-server` 1.43.1, and the server
failed on a module the older SDK does not have.

## What was built

`RunnerAgentServer` — the ninth execution backend, and the second (after the
external-application one) that starts nothing here. A registry of servers
(`agent_servers`), an administrator page to register and check them, a placement
rule, and the adapter that holds one conversation:

    POST /api/conversations                       → 201, conversation id
    GET  /api/conversations/{id}                  → execution_status, stats
    GET  /api/conversations/{id}/agent_final_response
    GET  /api/conversations/{id}/events/search    → what the agent did
    POST /api/conversations/{id}/pause            → on the way out, however it ends

A Goal names either one server or one network zone, never both: pinning a machine
usually encodes a reason nobody could put in a field, so a pin that quietly sends
work elsewhere is worse than a refusal. Placement skips servers that are disabled
or failed their last check, prefers a checked server over an unchecked one, and
refuses rather than crossing into another zone.

## What was established by driving it

Against a real 1.43.1 server, with a gateway standing in this test process on an
address the server had to reach over the network:

- The conversation completes and the answer comes back — `AGENTHUB-SERVER-OK`.
- **The model call arrives at this deployment's gateway carrying this
  deployment's credential.** Removing `base_url` from the start request — what an
  environment-variable integration amounts to — makes the run fail instead of
  silently going to the vendor. That mutation was run.
- Usage is real: the server's own `stats.usage_to_metrics` is what the run is
  metered on, so a run is never billed as free because nobody counted.
- History reaches this platform's timeline. `events/search` caps `limit` at 100
  and answers 500 above it, which is how the first version's 200 was found; the
  adapter pages within the cap and says so when it stops.

Two smaller facts, both cheap to lose: the batch `GET /api/conversations`
requires `ids` (listing is `/search`), and a Go test listener must be `tcp4` — the
wildcard gives a dual-stack socket some host networks do not publish outward,
which looks exactly like the agent being pointed somewhere else.

## What is not established yet

Forking and condensation were read from the contract rather than exercised. The server has its own approval gate and AgentHub already has
one; only one of them can be the authority and it has to be this platform's,
which is why conversations are created with the confirmation policy left at
never and the platform's own approval is what a task waits on. Forking and condensation are still unused.

## The pool, once there were two machines

Placement was written against one server and checked against two, which is where
its bugs were. Both were found by running it, not by reading it:

- **Capacity was decoration.** The field was registered and never counted
  against. It now is, from the runs in flight rather than from a counter, so a
  worker that dies does not leave a machine looking permanently full.
- **Counting is not claiming.** Two tasks queued together were placed three
  milliseconds apart, both read a load of zero, and both went to the machine
  limited to one at a time. The claim now locks the server's row and counts
  inside that transaction; the same two tasks then land on different machines.
  Choosing and taking stayed separate — a machine can fill between them — so a
  refused claim sends the placement back to choose among what is left.

Where a run executed is kept on the run. A pool changes shape, and "where did
this actually run" is asked about one run long afterwards.

## It is a runtime type as well, since 0.146.0

The backend could only be pointed at a machine somebody else had installed and
registered by URL, which made it the one execution path nobody could try without
first standing up a server. It is now also a runtime type: `agenthub-openhands`
carries the 1.43.1 server, and an agent whose runtime type is OpenHands works on
its own Pod.

The image is built from the three facts above rather than from the metadata —
uv rather than pip, `libtmux` and `tmux` installed, `openhands-tools` alongside
the server, all three packages pinned to one version — and the built image was
checked by this platform's own client: registered as a server, asked to check
itself, and answered healthy.

An agent with its own runtime never consults the registry. The registry's
capacity limits are about machines several agents share; a runtime's capacity is
its own Pod, so claiming a shared slot for it would refuse work against a limit
that is not about it.

## The approval gate, exercised

Run on a platform-spawned runtime, against a gateway inside the same cluster
that answers the first tool-bearing request with a tool call:

1. The agent asks to run `terminal`; the conversation stops at
   `waiting_for_confirmation` rather than acting.
2. AgentHub records a pending approval carrying the tool, the server and the
   arguments — `{"command":"echo AGENTHUB-APPROVED"}` — so the person deciding
   sees what would run rather than only that something would.
3. Approving it through the API sends the answer back; the conversation resumes,
   makes its next model call, and the task completes.

The server's own gate stays at `NeverConfirm` throughout. Only one authority can
decide, and it is this platform's.

A note for whoever tests this next: the first model call a conversation makes
carries no tools — the server is asking for a title. A stub that answers the
*first* call with a tool call spends it there and the agent never tries to act.
Answer the first request that actually offers tools.
