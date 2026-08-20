#!/bin/sh
# Run the agent headlessly for one task.
#
# The platform reaches a terminal agent by executing a command inside its
# container, and that command has no shell, no working directory and no profile:
# it starts in whatever the image's entrypoint left behind. This wrapper is the
# contract for those three things, so the caller passes plain arguments and never
# a shell string — a task title with a quote in it must not become a command.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

# The agent's own environment first, then the toolchain the image ships: a run
# has to see whatever the person installed while working in this same workspace.
PATH="${HOME:-/home/agent}/.venv/bin:/opt/agenthub/venv/bin:${HOME:-/home/agent}/.npm-global/bin:${PATH}"
export PATH

exec qwen "$@"
