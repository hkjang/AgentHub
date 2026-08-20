#!/bin/sh
# Prepare a BrowserCode runtime, then report what landed.
#
# The generated configuration carries the model binding and the bound MCP
# servers, in the shape this agent inherited from OpenCode. It replaces what is
# there rather than merging: the platform owns this file, and a stale copy from a
# previous binding is worse than none.
#
# The browser profile is created here so the first thing the agent does is not
# create it as a side effect. It is on the home volume, so cookies and logins a
# person established survive the Pod.
set -eu

BCODE_CONFIG_HOME="${BCODE_CONFIG_HOME:-/home/agent/.config/bcode}"
BROWSER_PROFILE="${AGENTHUB_BROWSER_PROFILE:-/home/agent/.chrome-profile}"
IMAGE_VENV="${AGENTHUB_IMAGE_VENV:-/opt/agenthub/venv}"
AGENT_VENV="${AGENTHUB_AGENT_VENV:-/home/agent/.venv}"

mkdir -p "$BCODE_CONFIG_HOME" "$BROWSER_PROFILE" /home/agent/.local/share/bcode

if [ ! -x "$AGENT_VENV/bin/python" ] && [ -x "$IMAGE_VENV/bin/python" ]; then
  if "$IMAGE_VENV/bin/python" -m venv "$AGENT_VENV" >/dev/null 2>&1; then
    image_site=$("$IMAGE_VENV/bin/python" -c 'import sysconfig; print(sysconfig.get_path("purelib"))' 2>/dev/null || true)
    agent_site=$("$AGENT_VENV/bin/python" -c 'import sysconfig; print(sysconfig.get_path("purelib"))' 2>/dev/null || true)
    if [ -n "$image_site" ] && [ -n "$agent_site" ] && [ -d "$agent_site" ]; then
      printf '%s\n' "$image_site" > "$agent_site/agenthub-toolchain.pth"
    fi
  else
    echo "agenthub: the agent virtualenv could not be created; pip install will need one under /workspace" >&2
  fi
fi

if [ -f "${AGENTHUB_BCODE_CONFIG:-/etc/agenthub/bcode.json}" ]; then
  cp "${AGENTHUB_BCODE_CONFIG:-/etc/agenthub/bcode.json}" "$BCODE_CONFIG_HOME/bcode.json"
fi

/usr/local/bin/agenthub-report-config "$BCODE_CONFIG_HOME/bcode.json" || true
