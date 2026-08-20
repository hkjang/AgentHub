#!/bin/sh
# Run Goose for one task, as a protocol peer rather than as a terminal.
#
# The platform reaches this agent by executing a command inside its container,
# and that command has no shell, no working directory and no profile: it starts
# in whatever the image's entrypoint left behind. This wrapper supplies all three,
# so the caller passes plain arguments and never a shell string.
#
# GOOSE_MODE=approve is the reason this file matters. Goose decides for itself
# what it may run unless it is told to ask, and told to ask it sends the client a
# session/request_permission before every tool call — which is what lets the
# platform answer according to the Goal and record what it answered. Verified
# against the real binary: without this, it edits files without a word.
#
# The interactive session a person opens is deliberately left alone: there is
# somebody there to answer, and that is what the mode they configured is for.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

# The agent's own environment first, then the toolchain the image ships: a run
# has to see whatever the person installed while working in this same workspace.
PATH="${HOME:-/home/agent}/.venv/bin:/opt/agenthub/venv/bin:${PATH}"
export PATH

GOOSE_MODE=approve
export GOOSE_MODE

exec goose "$@"
