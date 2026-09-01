#!/bin/sh
# The browser terminal for this runtime: prime-agent with the platform's provider
# already written, so anything tried by hand behaves the way a scheduled task will.
set -eu
cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
PATH="${HOME:-/home/agent}/.npm-global/bin:${PATH}"
export PATH
/usr/local/bin/agenthub-primeagent-configure >&2 || true
echo "Prime Agent — 'prime-agent --provider agenthub' 로 이 배포의 게이트웨이를 통해 실행됩니다."
exec /bin/bash -l
