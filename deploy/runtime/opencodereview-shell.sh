#!/bin/sh
# The browser terminal for this runtime.
#
# A review engine is not a chat, so what a person gets here is a shell in the
# workspace with `ocr` on PATH and the connection already prepared — the same
# connection the platform's own runs use, so anything tried by hand behaves the
# way a scheduled review will.
set -eu
cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"
/usr/local/bin/agenthub-opencodereview-configure >&2 || true
echo "Open Code Review — 'ocr review --preview' 로 대상 파일만 먼저 볼 수 있습니다."
exec /bin/bash -l
