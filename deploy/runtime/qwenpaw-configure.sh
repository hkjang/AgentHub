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

# The administrator's overlay, applied after `qwenpaw init` has created the file:
# merging before it exists would be overwritten by the initialiser itself.
OVERLAY_FILE="${AGENTHUB_QWENPAW_OVERLAY:-/etc/agenthub/qwenpaw-overlay.json}"
if [ -f "$OVERLAY_FILE" ] && [ -f "$QWENPAW_HOME/config.json" ]; then
  CONFIG_FILE="$QWENPAW_HOME/config.json" OVERLAY_FILE="$OVERLAY_FILE" \
  "$QWENPAW_PYTHON" - <<'OVERLAY' || echo "qwenpaw: overlay could not be applied" >&2
import json, os, pathlib

config_path = pathlib.Path(os.environ["CONFIG_FILE"])
overlay = json.loads(pathlib.Path(os.environ["OVERLAY_FILE"]).read_text(encoding="utf-8"))
config = json.loads(config_path.read_text(encoding="utf-8"))

def merge(base, patch):
    # Objects merge key by key so setting one field leaves the rest of its section
    # alone; anything else replaces, because a site that writes a list means it.
    for key, value in patch.items():
        if isinstance(value, dict) and isinstance(base.get(key), dict):
            merge(base[key], value)
        else:
            base[key] = value

# The platform owns the provider binding; an overlay must not break the model.
for reserved in ("providers", "active_model"):
    overlay.pop(reserved, None)
merge(config, overlay)
config_path.write_text(json.dumps(config, indent=2, ensure_ascii=False), encoding="utf-8")
print(f"qwenpaw: applied {len(overlay)} setting(s) from the overlay", file=__import__("sys").stderr)
OVERLAY
fi

# Report what is on disk before the model is bound, so a Pod that cannot reach its
# model endpoint still tells the platform which settings were applied.
/usr/local/bin/agenthub-report-config "$QWENPAW_HOME/config.json" || true

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

# `qwenpaw models config-key` and `models set-llm` are both interactive-only, so
# the key is written to the provider file the CLI maintains and the active model
# slot is selected through the same ProviderManager API the CLI drives. Without
# the slot the console starts on "Select model" and the agent cannot answer.
OPENAI_BASE_URL="$OPENAI_BASE_URL" \
OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
AGENTHUB_MODEL_NAME="$AGENTHUB_MODEL_NAME" \
PROVIDER_ID="$PROVIDER_ID" \
PROVIDER_FILE="$PROVIDER_FILE" \
"$QWENPAW_PYTHON" - <<'PY' || { echo "qwenpaw: could not bind the model" >&2; exit 0; }
import asyncio
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

from qwenpaw.providers.provider_manager import ProviderManager

manager = ProviderManager.get_instance()
asyncio.run(manager.activate_model(os.environ["PROVIDER_ID"], os.environ["AGENTHUB_MODEL_NAME"]))
active = manager.get_active_model()
if not active or not active.model:
    raise SystemExit("active model slot was not persisted")
PY

echo "qwenpaw: bound ${AGENTHUB_MODEL_NAME} at ${OPENAI_BASE_URL}" >&2
