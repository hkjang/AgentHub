#!/bin/sh
# Run one orca command against the runtime in this Pod.
#
# The platform reaches this runtime by executing a command in its container,
# with no shell, no working directory and no profile. This wrapper is the
# contract for those three, so the caller passes plain arguments — a branch name
# with a quote in it must not become a command.
#
# Every orchestration command is RPC to the running runtime and needs a sender
# terminal; the runner passes --from with a handle it created. That is Orca's
# contract, not something this wrapper can supply.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
PATH="${HOME:-/home/agent}/.local/bin:${PATH}"
export PATH

exec orca "$@"
