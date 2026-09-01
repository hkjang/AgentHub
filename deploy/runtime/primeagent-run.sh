#!/bin/sh
# Run prime-agent for one task.
#
# The platform reaches this runtime by executing a command in its container, with
# no shell, no working directory and no profile. This wrapper is the contract for
# those three, so the caller passes plain arguments — a task title with a quote in
# it must not become a command.
#
# The provider is written on every run rather than once at start: a runtime that
# was started before an administrator changed the gateway or the model would
# otherwise keep talking to the old one, and nothing would say so.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
PATH="${HOME:-/home/agent}/.npm-global/bin:${PATH}"
export PATH

/usr/local/bin/agenthub-primeagent-configure >&2

# The protocol owns stdout. The agent's own diagnostics go to stderr, and the
# configure step above is redirected there for the same reason: one stray line on
# stdout is a frame the client cannot parse.
exec prime-agent "$@"
