#!/bin/sh
# Point the coding agents this fabric can start at the AgentHub gateway.
#
# Inheriting the environment is not the same as honouring it. A worker terminal
# does inherit this container's variables — that was measured — but an agent only
# uses them if it reads them, and codex 0.149.0 does not: given
# OPENAI_BASE_URL it still opened wss://api.openai.com/v1/responses and failed
# with 401. A platform that had claimed the gateway on the strength of the
# variable would have been wrong in the one direction that matters, with every
# worker's prompt going straight to a vendor.
#
# What codex does honour is a provider in its own configuration, so that is what
# this writes. Each agent needs whatever mechanism that agent actually reads;
# there is no general answer, and an agent whose mechanism has not been
# established does not get a claim here.
set -eu

base="${AGENTHUB_MODEL_BASE_URL:-}"
model="${AGENTHUB_MODEL_NAME:-}"
home="${HOME:-/home/agent}"

if [ -z "$base" ]; then
  echo "agenthub: no model endpoint was given to this runtime; workers would reach a vendor directly" >&2
  exit 0
fi

# codex: a custom provider in ~/.codex/config.toml. `wire_api = "chat"` is
# refused by this version — it names `responses` in the error, which is the
# transport the AgentHub gateway speaks for this agent.
mkdir -p "$home/.codex"
# Every value is quoted. TOML refuses a bare string, and this file is read at
# agent start where the refusal reads as "Error loading config.toml" with no
# mention of the platform that wrote it.
{
  echo '# Written by AgentHub. The gateway is this deployment'"'"'s model endpoint:'
  echo '# policy, content inspection, quota and audit all sit behind it, and an'
  echo '# agent that talked to a vendor directly would be outside every one.'
  echo 'model_provider = "agenthub"'
  if [ -n "$model" ]; then
    echo "model = \"$model\""
  fi
  echo ''
  echo '[model_providers.agenthub]'
  echo 'name = "AgentHub Gateway"'
  echo "base_url = \"$base\""
  echo 'env_key = "OPENAI_API_KEY"'
  echo 'wire_api = "responses"'
} > "$home/.codex/config.toml"

echo "agenthub: codex points at $base" >&2
