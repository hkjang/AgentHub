#!/bin/sh
# Run BrowserCode for one task, as a protocol peer rather than as a terminal.
#
# The platform reaches this agent by executing a command inside its container,
# and that command has no shell, no working directory and no profile. This
# wrapper supplies all three, so the caller passes plain arguments and never a
# shell string.
#
# The browser this agent drives is already running beside it — the container's
# main process starts it — so there is nothing to launch here. What the agent
# needs is to be told where it is, and that arrives through the instructions file
# named in its configuration.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

PATH="${HOME:-/home/agent}/.venv/bin:/opt/agenthub/venv/bin:${PATH}"
export PATH

# This agent reports usage traces outward by default. An offline site must not,
# and a site that is not offline has not agreed to it either.
DO_NOT_TRACK=1
export DO_NOT_TRACK

exec bcode "$@"
