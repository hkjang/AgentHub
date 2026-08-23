#!/bin/sh
# The browser terminal for this runtime.
#
# What a person gets is a shell with `orca` on PATH, talking to the same runtime
# a scheduled task uses — so anything tried by hand behaves the way the platform's
# own runs will.
set -eu
cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
PATH="${HOME:-/home/agent}/.local/bin:${PATH}"
export PATH
echo "Orca — 'orca status --json' 으로 런타임 상태를, 'orca worktree list' 로 작업 사본을 볼 수 있습니다."
exec /bin/bash -l
