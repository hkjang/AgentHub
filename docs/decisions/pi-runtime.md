# Pi as an RPC-driven runtime

Verified 2026-08-23 against `@earendil-works/pi-coding-agent` 0.84.2, installed
from npm and run in a container. Everything below was observed, not read.

## The gateway is enforced by configuration, not by environment

This is the second agent in a row where that distinction decided the
integration, so it is now a rule rather than a surprise. Given
`OPENAI_BASE_URL` pointing at a local gateway, Pi went to OpenAI:

```
$ OPENAI_BASE_URL=http://127.0.0.1:8960/v1 OPENAI_API_KEY=agenthub-key \
    pi --provider openai --model gw-model -p "say hi"
OpenAI API error (401): Incorrect API key provided: agenthub-key.
    You can find your API key at https://platform.openai.com/...
```

The gateway's request log stayed empty. What Pi honours is a provider in
`~/.pi/agent/models.json`:

```json
{"providers":{"agenthub":{
  "baseUrl":"http://gateway/v1","api":"openai-completions",
  "apiKey":"<platform-issued>",
  "compat":{"supportsDeveloperRole":false,"supportsReasoningEffort":false},
  "models":[{"id":"<model>"}]}}}
```

With that, the requests arrive:

```
POST /v1/chat/completions  auth=Bearer agenthub-issued-key
```

`compat` is set because an OpenAI-compatible gateway is not OpenAI: Pi's own
documentation names these two flags for exactly this case, and without them it
sends a `developer` role and a `reasoning_effort` that such a server may refuse.

## RPC is a real protocol, and a rich one

`pi --mode rpc` reads JSONL commands on stdin and writes JSONL events on stdout.
One prompt produced:

```
{"type":"response","command":"prompt","success":true}
{"type":"agent_start"} {"type":"turn_start"}
{"type":"message_start", ...} {"type":"message_update", ..., "assistantMessageEvent":{"type":"text_delta","delta":"…"}}
{"type":"message_end", ...} {"type":"turn_end", ..., "toolResults":[]}
{"type":"agent_end", ..., "willRetry":false}
{"type":"agent_settled"}
```

Two things there matter to this platform.

**Usage is real and per message.** Every `message_update` and `message_end`
carries `input`, `output`, `cacheRead`, `cacheWrite`, `totalTokens` and a `cost`
breakdown. A run through this backend can be metered from the agent's own
numbers rather than described as unmetered.

**`agent_settled` is the end.** `agent_end` carries `willRetry`, so a turn that
ends is not necessarily the work ending; `agent_settled` is what says nothing
further is coming. A runner that stopped at `agent_end` would cut off a retry.

## The state query is also a check

`{"type":"get_state"}` answers with the model, the thinking level, the streaming
and compaction flags, the session file and id, the message count — and the
model's `baseUrl`:

```json
"model":{"id":"gw-model","provider":"agenthub","baseUrl":"http://127.0.0.1:8961/v1", ...}
```

So the platform can do more than configure the endpoint: it can ask a running
agent where it is actually pointed. That is a check worth having, given that the
environment variable it would have trusted turned out to mean nothing here.

## Sessions are files, and they are addressable

`get_state` names `sessionFile` and `sessionId`, and the CLI takes `--session`,
`--session-id`, `--fork` and `--session-dir`. Sessions live under
`~/.pi/agent/sessions/<project>/` as JSONL. Putting that directory on the
runtime's home volume is what makes a restarted Pod continue rather than begin.

## What is not established yet

Steering, follow-up, abort and compaction were not driven here — only `prompt`
and `get_state`. Project trust (`--approve` / `--no-approve`) was not exercised
either; it should be the platform's decision rather than the agent's, and until
that is tested the runner passes the conservative one.
