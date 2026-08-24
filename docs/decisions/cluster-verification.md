# What has been run on a real cluster, and how

Reading a contract and running it against a cluster are different kinds of
knowledge, and this session's bugs were all of the second kind: a model name
LiteLLM refuses, an agent whose error was recorded as an answer, a definition in
the cluster older than the build. None of them are visible in a unit test,
because none of them are wrong in the code — they are wrong in the meeting
between the code and something else.

This is the rig that produced them and what it has established, so the next
person can redo it rather than rediscover it.

## The rig

- **minikube**, with the operator running in `agent-platform-system` and
  runtimes in `agent-runtime-dev`. The control plane and worker run as
  containers on the host, sharing a docker network so they can resolve the
  names the test needs.
- **A gateway inside the cluster.** The host is a WSL distro and minikube runs
  in Docker Desktop's VM, so the runtime cannot reach a gateway on the host at
  any address. A Deployment plus Service in the same namespace is both the fix
  and the shape a real deployment has.
- **A network profile that allows it.** The default `Restricted` profile permits
  DNS and nothing else, which is why a runtime under it cannot call a model at
  all. The profile used here allows the pod network on the gateway's port.
- **A fake forge** as a container on the shared network, answering `/api/v1/user`
  and keeping what was posted to it.

## What the gateway has to do

A stub that returns one JSON object is not enough, and each shortfall looks like
a platform bug until it is understood:

- **Stream when asked.** A CLI agent that requested SSE and receives one JSON
  object reports `[API Error: Streaming request received a non-SSE response]` —
  and then exits 0, which is how a run that did nothing came to be recorded as
  completed.
- **Answer in the caller's vocabulary.** The review engine offers `code_comment`
  and `task_done`; prose back to it means "1 of 1 selected item(s) failed". The
  tool schema is in the request, so a stub can read what is expected rather than
  guess.
- **Do not spend the first call.** A conversation's first model call carries no
  tools: the server is asking for a title. A stub that answers the first call
  with a tool call spends it there and the agent never tries to act.

## What has been established

| Path | Established |
| --- | --- |
| OpenHands as a runtime type | Pod runs 2/2, server answers `/health`, an agent's work goes to its own runtime |
| `agentserver` backend | conversation completes, answer returned, usage metered from the server's own report |
| The approval gate | agent stops before acting, AgentHub holds a pending approval carrying the command, approving resumes it |
| `cli` backend | task completes with the agent's answer and real token usage; an agent that could not call the model now fails instead of completing |
| `acp` backend | task completes, usage metered |
| `review` backend | one file reviewed, finding stored with path, line, severity, category and suggestion; coverage recorded complete |
| Forge webhook | a Gitea-signed payload creates a task carrying the pull request's address |
| Posting back | the finding is commented on that pull request, and the next review edits the same comment rather than adding one |
| Forge credential check | a stored token is checked at save time and the account name comes back |
| `orca` backend | the fabric creates its run, task and worktree in the runtime's own repository — after the mount that made that possible |
| readiness screen | a runtime deleted from the cluster is written off rather than reported as stuck — measured against a record twelve minutes stale |
| `rpc` backend | task completes with the agent's answer and real token usage; the agent declares its own tools to the gateway |
| Content inspection | a card number in a task's input is blocked before it reaches the agent, the run says which class was found, and the task is not retried |
| Refusing a tool | rejecting the approval stops the tool: the file is not written, and the run says the refusal is why the agent had nothing to say |
| Tool approval on an in-Pod agent | with `사람 승인 요구` on, the agent's write is held: the run waits, AgentHub raises a pending approval naming the file, approving it lets the tool run — the file appears in the Pod — and the task completes |
| Cancelling a running task | the stop button ends an in-Pod agent — checked on `rpc` and on `acp`, with the agent genuinely working first: the task and the run both read cancelled and no agent process is left in the Pod |

Not established: forking and condensation on the agent server, which the
platform does not use.

Two things this round is worth keeping:

- **The fabric could not see the workspace.** Orca runs as a sidecar so a closed
  shell does not take the workers with it, and it had every mount except the
  repository — so every task was refused with "Not a valid git repository:
  /workspace", the fabric's own words about a directory that really was empty.
  Nothing was wrong except the mount, and nothing but running it would have said
  so.
- **A protocol backend has to hear more than its agent.** The rpc loop read with
  a blocking scan, so an agent waiting on a model that never answered kept the
  loop — and the run, and the process in the Pod — alive through a cancellation
  and past its own time limit. The acp client was already built the other way,
  waiting on cancellation, a closed connection and the answer at once. Checking
  a protocol backend means cancelling it while its agent is silent, not while it
  is talking.
- **Check that the agent was actually running.** The first attempt at the acp
  cancellation checked a Pod stuck in ImagePullBackOff: the task cancelled
  cleanly because nothing had started. A cancellation test that does not first
  see the agent working proves nothing.
- **Cancelling needs no per-backend work in a Pod.** The claim-keeper notices a
  cancelled task and cancels the run's context, which ends the exec — verified
  by cancelling a Pi task mid-run and finding no agent process left. The agent
  server backend needs its own check only because its work happens on a machine
  that context cannot reach.

## What it takes to exercise an approval gate

Three attempts proved nothing before one proved something, and each failure is
worth knowing:

1. **The agent never asked for a tool.** The stub gateway answers a tool call
   only on the first request that carries tools, and its process had served many
   by then. A run that completes without an `acp.permission` event has not
   tested the gate.
2. **The tool it asked for needed nobody's permission.** Answering with the
   first declared tool picked `agent`, a sub-agent launcher. A gate is exercised
   by a tool with a side effect — a write, a shell.
3. **The agent streams, and the stub's streaming branch could not ask.** Its
   tool-call path sat behind the non-streaming branch, unreachable for every
   agent that sets `stream: true`, which is most of them.

And one thing that looked like a platform bug and was not: `approvalMode` says
what is allowed, while `approvalRequired` is what routes a request to a person.
A Goal with a strict mode and no `approvalRequired` denies the tool itself and
asks nobody, which is what it is for.

## What the orca fabric needs before it can be verified

Three separate things had to be true before an orca run could be judged at all,
and each of them cost a run that proved nothing:

1. **Git has to work in the workspace.** A mounted volume's root belongs to
   root, and the agent is uid 10000, so git called the repository the platform
   had cloned for it somebody else's and refused every command. The fabric
   reported this as "Could not resolve a default base ref for this repo". Fixed
   in the Pod, not the rig — the runtime now names its own workspace and home as
   directories git may trust.
2. **The workers' model endpoint has to speak the Responses API.** The codex
   configuration AgentHub writes sets `wire_api = "responses"`, and the stub
   gateway answered every path with a `chat.completion` body. Codex could not
   read it, so its workers sat in `workerState: ready` for ever and every run
   ended at its time limit. The stub now serves `/v1/responses`, both streamed
   and not; `codex exec` completes a turn against it.
3. **The agent must not be waiting on a keypress.** The fabric refuses to start
   an agent whose terminal is blocked, and codex offers an update the moment a
   newer one is published — so an image that ran workers the day it was built
   stops running them days later with `Agent startup blocked:
   codex-update-prompt`. The runtime now answers that offer at start, in
   `deploy/runtime/orca-agents-configure.sh`.
4. **The Goal's time limit has to be longer than a worker takes.** The limit is
   the fabric's deadline for the whole fan-out, and a run that hits it reports a
   timeout rather than anything about the answer.

**Proven since:** a fabric answer carrying sensitive text is refused before it
is stored. Once the platform gathered what its workers said, a card number in a
worker's own words produced an empty step, the refusal recorded beside the
cancellation, and no trace of the number in any step or event.

**Why a worker rarely settles here:** the fabric tells each worker to report
with `worker_done --outcome succeeded|failed`, exactly once. A real agent does
it; the stub gateway answers one line and stops, so its workers stay `ready`
until the Goal's limit. That is the rig, not the platform.
