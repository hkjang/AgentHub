# Orca as an execution fabric

Verified 2026-08-23 against `orca-linux.AppImage` v1.4.188 from
github.com/stablyai/orca, run in a plain Docker container with no desktop
session. Everything below was observed, not read.

## It runs headless, which is the question that mattered

Maka is blocked because its model connection can only be repaired
interactively. Orca is not: a headless server is a documented, supported
deployment with a versioned JSON contract for supervisors.

```
$ ./squashfs-root/AppRun serve --port 6768 --json
{"type":"orca_server_ready","schemaVersion":1,"runtimeId":"...",
 "boundEndpoint":"ws://0.0.0.0:6768","advertisedEndpoint":"ws://127.0.0.1:6768",...}
```

It starts Xvfb itself when `DISPLAY` is unset, and the AppImage runs extracted
(`--appimage-extract`) so no FUSE device is needed — which is what makes a
container work. It also installs its own CLI on first serve, at
`~/.local/bin/orca` and `~/.local/bin/orca-ide`.

The container needs Electron's libraries. Established by adding them until it
started, in this order: `libglib2.0-0t64 libnss3 libatk1.0-0t64
libatk-bridge2.0-0t64 libcups2t64 libdrm2 libxkbcommon0 libxcomposite1
libxdamage1 libxfixes3 libxrandr2 libgbm1 libpango-1.0-0 libcairo2
libasound2t64 libgtk-3-0t64`, plus `xvfb`. D-Bus is not required — it complains
and continues.

## The whole chain works from a shell

```
$ orca status --json                     → runtime.state "ready", reachable true
$ orca repo add --path ~/repo --json     → repo id
$ orca worktree create --repo id:<id> --name agenthub-probe --json
    → path /home/orcauser/orca/workspaces/repo/agenthub-probe
      branch refs/heads/agenthub-probe          ← its own checkout, its own branch
$ orca terminal create --worktree worktree:<id> --command '...' --json
    → handle term_1a36c999-...
$ orca orchestration run-create --from <handle> --objective '...' --json
    → run_aa0c4e5be95e
$ orca orchestration task-create --from <handle> --task-title '...' --spec '...' --json
    → task_9bfdb0bfe27a
$ orca orchestration task-list --from <handle> --json
    → the task, with run_id, created_by_terminal_handle, created_by_pane_key,
      created_by_process_incarnation
```

The experimental-feature note in Orca's own orchestration guide did not block
any of this; the RPC answered from the first call.

## Two contract details worth writing down

Orchestration commands are RPC to the running runtime and every one of them
needs a **sender terminal**. Without `--from <handle>` (or `ORCA_TERMINAL_HANDLE`
in the environment) the answer is:

```
{"ok":false,"error":{"code":"no_active_sender_terminal", ...}}
```

So the platform creates a terminal first and speaks as it. And a Run must be
bound before tasks exist: `task-list` without one answers `run_required` rather
than an empty list — which is the good kind of refusal, since an empty list and
"you never bound a Run" mean opposite things.

Flag names are their own: `run-create` takes `--objective` not `--title`,
`task-create` takes `--task-title` and requires `--spec`. Errors carry
`data.validFlags`, so a wrong flag is a listed answer rather than a guess.

## Why this is a runner and not a twelfth runtime

Orca's value is that one task fans out to several coding agents, each in its own
git worktree, and the results are compared. A runtime type is one Pod running one
agent; that shape cannot express fan-out. So `orca` is an execution backend —
AgentHub keeps policy, quota, DLP, audit, the model gateway and the final verdict,
and Orca owns worker coordination, worktrees and dispatch inside one task.

It is also offered as a runtime type, because a person wants somewhere to open a
terminal and drive `orca` by hand against the same runtime a scheduled task uses.

## What is not established yet

The fan-out itself was not run here: `worker-start` launches a coding agent
(codex, claude, opencode) and none is installed in the probe container — the
runtime logged `spawn codex ENOENT` at startup and continued. What is proved is
the orchestration layer AgentHub drives, not the agents Orca drives. That is the
next thing to establish, with one real agent, before anything claims a
best-of-N.

## The fan-out, and the two things it needs

Followed up 2026-08-23. `worker-start` is the fan-out primitive and takes exactly
the shape the integration wants:

```
orca orchestration worker-start --task <id> --agent <agent> \
  --worktree new-child --name <name> [--model <id>] [--effort <level>]
```

One task, N workers, each getting its own checkout. Two refusals were met on the
way and both are contract, not accident:

- `consumer_fenced` — worker-start must come from the coordinator terminal bound
  to that Task's Run, not any terminal. So the runner has to keep the handle it
  created rather than looking one up.
- `agent_unconfigured` — the agent must be configured on the host. The agents are
  `claude` and `codex`, and `orca account add --agent claude|codex` runs the
  vendor's own login interactively.

`agent_unconfigured` came from passing an agent name the fabric does not know,
not from a missing account — an earlier note here said otherwise and was wrong.

## What actually happens when the agent is not installed

Followed up further, and this is the finding that matters for the runner.
`worker-start --agent claude` on a host with no Claude installed answers:

```json
{"ok":true, ...}
```

It creates the checkout (`agenthub-abc123-claude`) and a dispatch, and returns.
The worker then dies in its own terminal with a shell error, and the fabric
records that honestly — but only where somebody asks:

```
$ orca orchestration worker-show --dispatch ctx_89f6893800fe --json
"status":"failed", "last_failure":"agent_prompt_stalled",
"dispatched_at":"2026-08-23 05:48:20", "completed_at":"2026-08-23 05:48:38"
```

So **an accepted dispatch is not a running worker.** A runner that counts the
`ok` reports starting workers that never ran — which is the exact class of claim
this platform keeps removing, and the first version of this runner did it. It
asks `worker-show` now and records what the fabric says became of each dispatch.

The fan-out is therefore still not demonstrated end to end: the checkouts and
dispatches are real, the workers need the agent installed and logged in on the
host. What is different from the earlier note is where the wall is — not at
`worker-start`, which accepts, but eighteen seconds later in the worker's own
terminal.

## Model calls do go through the gateway

This was the one thing that would have made the integration unacceptable, and it
holds. A terminal the fabric creates inherits the runtime container's
environment. Read back out of a live worker terminal:

```
$ echo GW=$OPENAI_BASE_URL AN=$ANTHROPIC_BASE_URL AH=$AGENTHUB_MODEL_BASE_URL
GW=http://gateway.agenthub.svc/v1 AN=http://gateway.agenthub.svc/v1 AH=http://gateway.agenthub.svc/v1
```

Those are the names Codex and Claude Code read for their endpoint, so pointing
them at the AgentHub gateway on the runtime container puts every worker's model
call behind the same policy, content inspection, quota and audit as everything
else. `OPENAI_BASE_URL` is already in the shared runtime environment;
`ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY` are added by the Orca adapter,
because no other runtime needed them and without them a Claude worker would talk
to a vendor directly with nothing on this platform seeing the call.

What that does **not** do is remove the account requirement. The endpoint is the
platform's; the agent still refuses to start until an account exists on the host.
