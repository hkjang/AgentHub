#!/bin/sh
# Start the OpenHands Agent Server.
#
# The server is the whole runtime: AgentHub drives it over its REST API rather
# than by running a command and waiting. Nothing here writes a model or a
# credential, because this server takes both as fields on the request that starts
# a conversation — so one server can serve several deployments without being
# reconfigured, and a runtime started before an administrator changed the gateway
# does not go on talking to the old one.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

# Bound to every interface because the control plane reaches it across the Pod
# network. What may reach it is the network policy's business, and the platform's
# proxy sidecar is what publishes it to people.
exec python -m openhands.agent_server \
  --host "${AGENTHUB_OPENHANDS_HOST:-0.0.0.0}" \
  --port "${AGENTHUB_OPENHANDS_PORT:-8000}"
