#!/bin/sh
# Point Pi at the AgentHub gateway, and nowhere else.
#
# Inheriting the environment is not honouring it. Given OPENAI_BASE_URL, Pi went
# to OpenAI and failed with that vendor's own 401 while the gateway's request log
# stayed empty — the same thing codex does. What Pi honours is a provider in its
# own models.json, so that is what this writes.
#
# `compat` is set because an OpenAI-compatible gateway is not OpenAI: Pi's
# documentation names these two flags for exactly this case, and without them it
# sends a `developer` role and a `reasoning_effort` that such a server may refuse.
set -eu

base="${AGENTHUB_MODEL_BASE_URL:-}"
key="${AGENTHUB_MODEL_API_KEY:-${OPENAI_API_KEY:-}}"
model="${AGENTHUB_MODEL_NAME:-}"
home="${HOME:-/home/agent}"
config="$home/.pi/agent"

if [ -z "$base" ]; then
  echo "agenthub: no model endpoint was given to this runtime; pi would reach a vendor directly" >&2
  exit 0
fi
if [ -z "$model" ]; then
  echo "agenthub: no model name was given to this runtime; pi has nothing to select" >&2
  exit 0
fi

mkdir -p "$config"
# Written with a here-document rather than string interpolation into JSON: a
# model name with a quote in it would otherwise produce a file Pi refuses, and
# the refusal names the file rather than the platform that wrote it.
cat > "$config/models.json" <<JSON
{
  "providers": {
    "agenthub": {
      "baseUrl": "$base",
      "api": "openai-completions",
      "apiKey": "${key:-agenthub}",
      "compat": { "supportsDeveloperRole": false, "supportsReasoningEffort": false },
      "models": [ { "id": "$model" } ]
    }
  }
}
JSON

# The sessions are what make a restarted Pod continue rather than begin, so they
# live on the home volume with everything else Pi keeps.
mkdir -p "$config/sessions"

if [ -x /usr/local/bin/agenthub-report-config ]; then
  /usr/local/bin/agenthub-report-config provider=agenthub base_url="$base" model="$model" || true
fi
echo "agenthub: pi points at $base" >&2
