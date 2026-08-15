#!/bin/sh
# Initialise the QwenPaw working directory and bind the AgentHub model endpoint.
#
# Both the operator's init container and the standalone entrypoint call this, so
# the configuration steps live in exactly one place. Safe to re-run: every step
# is idempotent and a fresh Pod with a preserved home directory keeps its state.
set -eu

QWENPAW_HOME="${QWENPAW_HOME:-$HOME/.qwenpaw}"
QWENPAW_BIN="${QWENPAW_BIN:-/opt/qwenpaw/.venv/bin/qwenpaw}"
QWENPAW_PYTHON="${QWENPAW_PYTHON:-/opt/qwenpaw/.venv/bin/python}"
PROVIDER_ID=agenthub

mkdir -p "$QWENPAW_HOME"
if [ ! -f "$QWENPAW_HOME/config.json" ]; then
  "$QWENPAW_BIN" init --defaults --accept-security || true
fi

if [ -z "${OPENAI_BASE_URL:-}" ] || [ -z "${AGENTHUB_MODEL_NAME:-}" ]; then
  echo "qwenpaw: no model endpoint bound, skipping provider configuration" >&2
  exit 0
fi

PROVIDER_FILE="${QWENPAW_HOME}.secret/providers/custom/${PROVIDER_ID}.json"

# `models add-provider` does not fail on a duplicate id — it silently registers a
# second provider under "<id>-new". The init container and the agent container
# both run this script, and the home directory survives restarts, so guard on the
# provider file instead of retrying blindly.
if [ ! -f "$PROVIDER_FILE" ]; then
  "$QWENPAW_BIN" models add-provider "$PROVIDER_ID" \
    --name "AgentHub Model Gateway" --base-url "$OPENAI_BASE_URL" >/dev/null 2>&1 || true
fi
if ! grep -q "\"$AGENTHUB_MODEL_NAME\"" "$PROVIDER_FILE" 2>/dev/null; then
  "$QWENPAW_BIN" models add-model "$PROVIDER_ID" \
    --model-id "$AGENTHUB_MODEL_NAME" --model-name "$AGENTHUB_MODEL_NAME" >/dev/null 2>&1 || true
fi

# `qwenpaw models config-key` is interactive only, so the key is written to the
# same provider file the CLI maintains.
OPENAI_BASE_URL="$OPENAI_BASE_URL" \
OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
PROVIDER_FILE="$PROVIDER_FILE" \
"$QWENPAW_PYTHON" - <<'PY' || echo "qwenpaw: could not persist model credentials" >&2
import json
import os
import pathlib

path = pathlib.Path(os.environ["PROVIDER_FILE"])
if not path.exists():
    raise SystemExit("provider file was not created")
provider = json.loads(path.read_text())
provider["base_url"] = os.environ["OPENAI_BASE_URL"]
provider["api_key"] = os.environ.get("OPENAI_API_KEY", "")
path.write_text(json.dumps(provider, indent=2))
PY

echo "qwenpaw: bound ${AGENTHUB_MODEL_NAME} at ${OPENAI_BASE_URL}" >&2
