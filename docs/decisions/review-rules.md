# Site-managed review rules are not buildable yet

The Open Code Review priority list ends with *Review Rules 관리* — letting a site
write its own review standard rather than accepting the engine's. This records
what the engine actually offers, measured against version 1.9.9 in the image this
platform ships, and why the AgentHub side is deliberately absent.

## What the engine has

`ocr rules` exists and needs no model:

```
$ ocr rules check src/main.go
File: src/main.go
Source: System built-in
Pattern: **/*.go
Rule:
#### Go Review Principles
…
```

The built-in rules are markdown documents, one per language, selected by glob.
The selection table is embedded in the binary and has this shape:

```json
{
  "default_rule": "default.md",
  "path_rule_map": {
    "**/*.go": "go.md",
    "**/*.java": "java.md",
    "**/*{mapper,dao}*.xml": "mapper_dao_xml.md",
    ".github/workflows/**/*.{yaml,yml}": "github_workflows.md"
  }
}
```

`ocr rules check --rule <file>` takes a custom file of that shape, and
`--repo` points at the repository root. The command reports which rule applies,
which layer it came from, and the pattern that matched — everything a site would
need to see before a review runs.

## What could not be made to work

A custom rule file is read and validated: a file whose top level is an array
fails with `unmarshal rule file: json: cannot unmarshal array into Go value of
type rules.ProjectRule`, so the shape above is the shape it wants. But no
arrangement tried made a custom rule actually apply. In each case `rules check`
still answered `Source: System built-in`:

- the rule file outside the repository, referenced by absolute path
- the rule file inside the repository at `.ocr/rules.json`, referenced relatively
- a `path_rule_map` naming a markdown file beside the rule file
- a path no built-in pattern covers (`thing.acme`), which fell back to the
  system default rather than to the custom `default_rule`

So the file is parsed and then not selected. Something about how the markdown is
resolved, or which layer a file supplied this way belongs to, is not visible from
the binary or its `--help`. The format is documented at
open-codereview.ai/docs/review-rules, which this environment cannot reach.

## Why nothing was built

A screen for managing review rules that writes a file the engine ignores is the
exact failure this platform keeps removing: a control that looks applied and does
nothing. The console would show a site standard, an operator would believe
reviews were held to it, and every review would use the engine's own rules.

The AgentHub side is small once the mechanism is known — a rule set is a name, a
glob and a markdown body, materialised into the runtime before `ocr review` and
passed with `--rule`. It is deliberately absent until a custom rule can be shown
to win, the same way the Maka runtime descriptor is absent until Maka can be
driven headlessly.

## What would unblock it

One observation of a custom rule applying: `ocr rules check --rule <file> <path>`
answering with a source that is not `System built-in`. That is the whole test,
and it takes a minute once the correct layout is known.
