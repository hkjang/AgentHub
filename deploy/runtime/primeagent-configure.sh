#!/bin/sh
# Point Prime Agent at the AgentHub gateway, and nowhere else.
#
# Inheriting the environment is not honouring it: this agent selects a provider
# from its own models.json and ignores OPENAI_BASE_URL, the same as Pi, which was
# measured against a stub gateway rather than assumed. So a provider is what this
# writes.
#
# `compat` is set because an OpenAI-compatible gateway is not OpenAI: the agent's
# own documentation names these two flags for exactly this case, and without them
# it sends a `developer` role and a `reasoning_effort` such a server may refuse.
set -eu

base="${AGENTHUB_MODEL_BASE_URL:-}"
key="${AGENTHUB_MODEL_API_KEY:-${OPENAI_API_KEY:-}}"
model="${AGENTHUB_MODEL_NAME:-}"
home="${HOME:-/home/agent}"
config="${PRIME_AGENT_CODING_AGENT_DIR:-$home/.prime/agent}"

if [ -z "$base" ]; then
  echo "agenthub: no model endpoint was given to this runtime; prime-agent would reach a vendor directly" >&2
  exit 0
fi
if [ -z "$model" ]; then
  echo "agenthub: no model name was given to this runtime; prime-agent has nothing to select" >&2
  exit 0
fi

mkdir -p "$config"
# Written with a here-document rather than string interpolation into JSON: a
# model name with a quote in it would otherwise produce a file the agent refuses,
# and the refusal names the file rather than the platform that wrote it.
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
# live on the home volume with everything else the agent keeps.
mkdir -p "$config/sessions"

if [ -x /usr/local/bin/agenthub-report-config ]; then
  /usr/local/bin/agenthub-report-config provider=agenthub base_url="$base" model="$model" || true
fi
echo "agenthub: prime-agent points at $base" >&2
