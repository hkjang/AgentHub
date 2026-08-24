#!/bin/sh
# The shell a person gets when they open this runtime.
#
# The agent's own work happens through the server's API; this is for looking at
# what it did to the workspace.
set -eu
cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
exec /bin/bash -l
