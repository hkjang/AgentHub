#!/bin/sh
# Prepare a Qwen Code runtime, then report what landed.
#
# Three things have to be true before the agent is useful. It must know which
# model to talk to, which it reads from ~/.qwen/.env. It must know which tools it
# may call, which is the settings file the platform generates with the bound MCP
# servers in it. And `pip install` has to work — a coding agent that cannot add a
# package is half an agent — which on the default security profile it would not,
# because that profile mounts the root filesystem read-only and the toolchain the
# image ships lives there.
#
# So the agent's own environment is created on the home volume, which is writable
# and survives the Pod, and a .pth file points it back at the image's toolchain.
# Packages the agent installs land on the volume and take precedence; everything
# preinstalled stays importable.
set -eu

QWEN_HOME="${QWEN_CODE_HOME:-/home/agent/.qwen}"
IMAGE_VENV="${AGENTHUB_IMAGE_VENV:-/opt/agenthub/venv}"
AGENT_VENV="${AGENTHUB_AGENT_VENV:-/home/agent/.venv}"

mkdir -p "$QWEN_HOME"

if [ ! -x "$AGENT_VENV/bin/python" ] && [ -x "$IMAGE_VENV/bin/python" ]; then
  if "$IMAGE_VENV/bin/python" -m venv "$AGENT_VENV" >/dev/null 2>&1; then
    image_site=$("$IMAGE_VENV/bin/python" -c 'import sysconfig; print(sysconfig.get_path("purelib"))' 2>/dev/null || true)
    agent_site=$("$AGENT_VENV/bin/python" -c 'import sysconfig; print(sysconfig.get_path("purelib"))' 2>/dev/null || true)
    if [ -n "$image_site" ] && [ -n "$agent_site" ] && [ -d "$agent_site" ]; then
      # Appended by site.py after the venv's own packages, so an agent upgrading
      # something the image ships gets the version it installed.
      printf '%s\n' "$image_site" > "$agent_site/agenthub-toolchain.pth"
    fi
  else
    echo "agenthub: the agent virtualenv could not be created; pip install will need one under /workspace" >&2
  fi
fi

# The generated settings — model binding and the bound MCP servers — replace what
# is there rather than merging: the platform owns this file, and a stale copy
# from a previous binding is worse than none.
if [ -f "${AGENTHUB_QWEN_SETTINGS:-/etc/agenthub/qwen-settings.json}" ]; then
  cp "${AGENTHUB_QWEN_SETTINGS:-/etc/agenthub/qwen-settings.json}" "$QWEN_HOME/settings.json"
fi

# Credentials go in the agent's own .env rather than the settings file, which is
# where Qwen Code reads them from and which keeps them out of a file the platform
# regenerates.
if [ -n "${OPENAI_BASE_URL:-}" ]; then
  umask 077
  printf 'OPENAI_API_KEY=%s\nOPENAI_BASE_URL=%s\nOPENAI_MODEL=%s\n' \
    "${OPENAI_API_KEY:-}" "$OPENAI_BASE_URL" "${AGENTHUB_MODEL_NAME:-}" > "$QWEN_HOME/.env"
fi

/usr/local/bin/agenthub-report-config "$QWEN_HOME/settings.json" || true
