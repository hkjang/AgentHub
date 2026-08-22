# Apache Maka as a runtime: what was checked, and what blocks it

Maka is an obvious candidate for another headless runtime. It has a tool loop, a
non-interactive `maka run`, and — the part worth having — a Runtime Event Log with
recoverable execution history, which is a stronger execution record than anything
else the platform drives.

This note records what was verified, so the next person to look does not repeat
it. Everything below was checked against the published binary
(`maka-agent@next`, run in a container) or against the source at
`github.com/apache/maka`, not against documentation: the README and the CLI
package README both stop short of the flag surface.

## What `maka run` actually accepts

Read from `maka run --help` on the real binary:

```
maka run [PROMPT] [options]
  -                       Read the complete prompt from stdin
  --cwd <path>            Working directory
  --connection <slug>     Model connection to use
  --host <profile-id>     Connect through a saved Runtime Host profile
  --project <project-id>  Select an existing Project on a remote Host
  --model <id>            Model to use
  --thinking <level>      off|minimal|low|medium|high|xhigh|max|default
  --timeout <seconds>     Invocation timeout
  --max-steps <count>     Tool-step cap
  --yolo                  Give this task full access to your files and network
  --resume <session-id>   Continue an explicit compatible task
  --continue              Continue the latest compatible task for cwd
  --graph                 Run this turn in Graph Mode
```

Three consequences for this platform:

- **There is no JSON output.** Qwen Code's parser cannot be reused, and a Maka run
  reports no token count, so it is unmetered by construction rather than by
  omission.
- **There is no approval-mode flag.** `--yolo` grants full file and network
  access; without it, a sandbox boundary request in a non-interactive run is
  auto-denied rather than prompted. In a container that is the right default.
- The Goal's guardrails map cleanly otherwise: max steps to `--max-steps`, max
  duration to `--timeout`, the model to `--model`.

## What blocks it

A model connection cannot be configured for a headless run.

Setting `OPENAI_BASE_URL` and `OPENAI_API_KEY` does reach Maka: it synthesises a
connection called `env-openai` and probes the endpoint — a stub gateway recorded
`GET /v1/models` from inside the container. But the run then refuses:

```
maka run: Task is not ready:
model_model_not_enabled: repair connection "env-openai" in `maka`   (model: stub)
model_empty_model_list:  repair connection "env-openai" in `maka`   (model: gpt-4o-mini)
```

Three findings explain it and agree with each other:

1. The seed is hardcoded. `packages/runtime-host/src/server/bootstrap-runtime-policy.ts`
   builds the connection as
   `{slug: 'env-openai', providerType: 'openai', enabledModelIds: ['gpt-4o-mini'], secret: openai}`.
   No environment variable names the base URL, the model, or the enabled models.
2. Readiness is a pure check. `packages/core/src/connection-readiness.ts` evaluates
   the stored connection record — `enabled`, a usable secret, a non-empty model
   list, the model enabled and chat-capable — and fetches nothing. A gateway that
   serves the model does not make the record ready; the record has to already say
   so.
3. Nothing headless can write that record. The full command list is `run`,
   `activate`, `eval` and `runtime-host`; there is no `connection` or `config`
   command. `packages/storage/src/config-transfer.ts` is an import/export bundle
   with no CLI or file entry point — it is driven by the desktop UI. The remedy
   the product itself names, "repair connection in `maka`", is interactive.

Serving the model under the id `gpt-4o-mini` does not help: verified against a
stub that served exactly that id, and the connection was still not ready.

The Runtime Host path does not route around this. The bootstrap quoted above is
the Runtime Host's own, so a hosted Maka has the same unconfigurable connection.

## What would unblock it

In rough order of how much they ask of anybody:

- An environment variable for the enabled model ids, or seeding them from the
  endpoint's `/v1/models`. This is a small, well-defined change to the bootstrap
  above, and it is the one worth proposing upstream.
- A `maka connection set --slug … --base-url … --model …` command, which is what
  every other headless integration of this shape ends up needing.
- Depending on the hardcoded `gpt-4o-mini` seed. Not recommended even if it
  worked: it is an internal detail of an incubating project, and it would forbid
  choosing a model per agent, which is the platform's own model.

## What is ready on this side

The headless backend no longer speaks one agent's command line to every runtime
(see architecture.md). Adding Maka is now an adapter and a descriptor: its
vocabulary is known and written down above, and a runtime that offers the CLI
backend without an adapter fails a test rather than a run.

The descriptor is deliberately not added yet. A runtime somebody can select and
which cannot start is the failure this platform has spent a great deal of effort
removing, and it would be a poor thing to add on purpose.
