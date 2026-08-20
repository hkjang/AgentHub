#!/bin/sh
# Prepare a Goose runtime, then report what landed.
#
# Three things have to be true before the agent is useful. It must know which
# provider to talk to, which Goose reads from its own environment rather than
# from a file. It must know which tools it may call, which is the configuration
# the platform generates with the bound MCP servers in it — Goose calls them
# extensions. And `pip install` has to work, which on the default security
# profile it would not, because that profile mounts the root filesystem read-only
# and the toolchain the image ships lives there.
#
# So the agent's own environment is created on the home volume, which is writable
# and survives the Pod, and a .pth file points it back at the image's toolchain.
# Packages the agent installs land on the volume and take precedence; everything
# preinstalled stays importable.
set -eu

GOOSE_CONFIG_HOME="${GOOSE_CONFIG_HOME:-/home/agent/.config/goose}"
IMAGE_VENV="${AGENTHUB_IMAGE_VENV:-/opt/agenthub/venv}"
AGENT_VENV="${AGENTHUB_AGENT_VENV:-/home/agent/.venv}"

# Goose keeps its sessions and logs beside its configuration, all under the home
# volume so a conversation somebody had survives the Pod.
mkdir -p "$GOOSE_CONFIG_HOME" /home/agent/.local/share/goose /home/agent/.local/state/goose

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

# The generated configuration — provider, model and the bound extensions —
# replaces what is there rather than merging: the platform owns this file, and a
# stale copy from a previous binding is worse than none. It is written as JSON,
# which Goose's YAML parser reads, so the platform has one way of building
# configuration rather than two.
if [ -f "${AGENTHUB_GOOSE_CONFIG:-/etc/agenthub/goose-config.yaml}" ]; then
  cp "${AGENTHUB_GOOSE_CONFIG:-/etc/agenthub/goose-config.yaml}" "$GOOSE_CONFIG_HOME/config.yaml"
fi

/usr/local/bin/agenthub-report-config "$GOOSE_CONFIG_HOME/config.yaml" || true
