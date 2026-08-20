#!/bin/sh
# What a person sees when they open a HolmesGPT runtime.
#
# The product is an investigator you ask questions, so the browser session lands
# in an interactive session rather than at a shell prompt. When it exits — the
# person quit, or it could not start because the model binding is wrong — the
# session drops to a login shell instead of closing the tab, because a terminal
# that vanishes tells you nothing about why.
set -u

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

if command -v holmes >/dev/null 2>&1; then
  printf 'HolmesGPT — 조사할 내용을 물어보세요. 예) why is the checkout deployment unhealthy?\n\n'
  holmes ask --interactive || printf '\nholmes exited (%s). 아래 셸에서 다시 실행하려면 holmes ask -i 를 입력하세요.\n\n' "$?"
else
  printf 'holmes 명령을 찾을 수 없습니다. 이 이미지는 HolmesGPT 런타임용이 아닙니다.\n'
fi

exec bash -l
