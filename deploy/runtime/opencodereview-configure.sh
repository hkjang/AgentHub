#!/bin/sh
# Point Open Code Review at the AgentHub gateway, then say whether it answers.
#
# OCR keeps its model connection in its own configuration file, written by
# `ocr config set`. Every one of those commands is non-interactive — which is the
# reason this runtime exists at all, and the difference between this and a
# runtime that can be selected and never started.
#
# The connection is a *custom provider* rather than a built-in one, because the
# endpoint is the platform's gateway and not a vendor: that is what puts every
# review's model calls behind the same policy, DLP, quota and audit trail as the
# rest of AgentHub. The runtime never learns a provider's own key.
set -eu

base="${AGENTHUB_MODEL_BASE_URL:-}"
key="${AGENTHUB_MODEL_API_KEY:-${OPENAI_API_KEY:-}}"
model="${AGENTHUB_MODEL_NAME:-}"

if [ -z "$base" ]; then
  echo "agenthub: no model endpoint was given to this runtime; ocr has nothing to call" >&2
  exit 1
fi

# The gateway speaks the OpenAI protocol, so that is what OCR is told to speak.
ocr config set provider agenthub >/dev/null
ocr config set custom_providers.agenthub.url "$base" >/dev/null
ocr config set custom_providers.agenthub.protocol openai >/dev/null
if [ -n "$key" ]; then
  ocr config set custom_providers.agenthub.api_key "$key" >/dev/null
fi
if [ -n "$model" ]; then
  ocr config set model "$model" >/dev/null
fi

# Git older than 2.41 cannot produce the diffs OCR asks for, and the failure it
# causes reads as an empty review rather than as a missing dependency.
git_version=$(git --version 2>/dev/null | awk '{print $3}')
case "$git_version" in
  '') echo "agenthub: git is missing; ocr cannot read a diff without it" >&2; exit 1 ;;
  *)
    major=$(echo "$git_version" | cut -d. -f1)
    minor=$(echo "$git_version" | cut -d. -f2)
    if [ "$major" -lt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -lt 41 ]; }; then
      echo "agenthub: git $git_version is older than the 2.41 open-code-review requires" >&2
      exit 1
    fi
    ;;
esac

if [ -x /usr/local/bin/agenthub-report-config ]; then
  /usr/local/bin/agenthub-report-config provider=agenthub base_url="$base" model="${model:-unset}" git="$git_version" || true
fi
