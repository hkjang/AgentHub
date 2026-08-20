#!/bin/sh
# Prepare a HolmesGPT runtime, then report what landed.
#
# Holmes reads one configuration file for everything that matters here: which
# model to investigate with, where that model lives, and which data sources it
# may query — its toolsets and the MCP servers the platform bound to this agent.
# The generated file replaces what is there rather than merging, because the
# platform owns it and a stale copy from a previous binding is worse than none.
#
# `pip install` also has to work: an investigator that cannot add a library to
# parse somebody's log format is less useful than one that can, and on the
# default security profile it would not, because that profile mounts the root
# filesystem read-only and the toolchain the image ships lives there.
set -eu

HOLMES_HOME="${HOLMES_CONFIG_HOME:-/home/agent/.holmes}"
IMAGE_VENV="${AGENTHUB_IMAGE_VENV:-/opt/agenthub/venv}"
AGENT_VENV="${AGENTHUB_AGENT_VENV:-/home/agent/.venv}"

mkdir -p "$HOLMES_HOME"

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

# Written as JSON under a .yaml name: Holmes parses YAML, YAML is a superset of
# JSON, and the platform gets one way of building configuration rather than two.
if [ -f "${AGENTHUB_HOLMES_CONFIG:-/etc/agenthub/holmes-config.yaml}" ]; then
  cp "${AGENTHUB_HOLMES_CONFIG:-/etc/agenthub/holmes-config.yaml}" "$HOLMES_HOME/config.yaml"
fi

/usr/local/bin/agenthub-report-config "$HOLMES_HOME/config.yaml" || true
