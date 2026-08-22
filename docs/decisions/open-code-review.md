# Open Code Review as a review runner

Verified 2026-08-23 against the published `@alibaba-group/open-code-review`
v1.9.9 binary, run in a container against a real git repository and an
OpenAI-compatible stub. Everything below was observed, not read.

## Why this is not the Maka situation

Maka is blocked upstream: its model connection can only be repaired
interactively (see `maka-runtime.md`). OCR is the opposite — every part of the
integration AgentHub needs is a documented, non-interactive command.

```
$ ocr config set provider agenthub
$ ocr config set custom_providers.agenthub.url http://gateway/v1
$ ocr config set custom_providers.agenthub.protocol openai
$ ocr config set custom_providers.agenthub.api_key <key>
$ ocr config set model <model>
$ ocr llm test
Source: provider:agenthub
URL:    http://gateway/v1
Model:  <model>
✓ Connection test successful
```

That is the whole model connection, and it points at whatever OpenAI-compatible
endpoint it is given — which is how every model call goes through the AgentHub
gateway rather than out to a provider.

## What it returns

`ocr review --format json --audience agent` writes one JSON document. A run that
found something:

```json
{
  "status": "complete",
  "summary": {"files_reviewed": 1, "comments": 1, "total_tokens": 3060,
              "input_tokens": 2700, "output_tokens": 360},
  "tool_calls": {"total": 1, "by_tool": {"code_comment": 1}},
  "comments": [{
    "path": "internal/auth/token.go", "start_line": 13, "end_line": 13,
    "category": "security", "severity": "critical",
    "content": "...", "existing_code": "...", "suggestion_code": "..."
  }],
  "session_id": "...",
  "manifest": {"schema_version": "ocr.run-manifest/v1", ...}
}
```

The line number is OCR's own work: the model reports the code it is commenting
on, and OCR matches that text against the diff with a sliding window. The
platform does not have to trust a model's arithmetic about line numbers.

`manifest` is the part worth keeping. It carries the resolved base and head
commits, the exact range, a sha256 of the source artifact, the sha256 of the
rule and runtime configuration, and coverage — every item selected, completed,
reused from a previous session, or failed, each with a fingerprint. That is a
review that can be shown to have covered what it says it covered.

## Categories and severities are fixed

From the `code_comment` tool schema the binary sends to the model:

- category: `bug`, `security`, `performance`, `maintainability`, `test`,
  `style`, `documentation`, `other`
- severity: `critical`, `high`, `medium`, `low`

AgentHub stores these verbatim rather than mapping them, so a finding means the
same thing in both systems.

## Modes

| What a person asks for | Command |
| --- | --- |
| the workspace as it stands | `ocr review` |
| a branch against a base | `ocr review --from main --to feature` |
| one commit | `ocr review --commit <sha>` |
| the whole repository | `ocr scan` |
| one directory | `ocr scan --path <dir>` |
| continue an earlier run | `... --resume <session-id>` |

Also observed: `--preview` lists the files that would be reviewed without
calling a model at all, `--exclude` takes gitignore-style patterns,
`--max-tokens-budget` caps a run's total tokens and reports what it skipped, and
`--concurrency` bounds parallel file reviews.

## Delegation mode needs no model

`ocr delegate preview` and `ocr delegate rule <files...>` output the file
selection and the resolved rules with no LLM configured at all:

```
# Files (1 reviewable / 1 total)
- mode: range
- from: master
- to: feature
- merge_base: 27f4fdf...
  - `internal/auth/token.go` [modified] +5/-0
```

This is the deterministic half on its own, which is what makes it possible to
let AgentHub choose the reasoning agent later while OCR remains the harness.

## Constraint found while building the image

OCR requires git 2.41 or newer. `node:22-bookworm-slim` ships 2.39.5, so the
image installs a newer git rather than assuming the base has one.
