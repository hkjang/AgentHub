#!/bin/sh
# What a person sees when they open a Goose runtime.
#
# The product is a terminal chat, so the browser session lands in it rather than
# at a shell prompt with instructions. When the agent exits — the person quit it,
# or it could not start because the provider binding is wrong — the session drops
# to a login shell instead of closing the tab, because a terminal that vanishes
# tells you nothing about why.
set -u

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

if command -v goose >/dev/null 2>&1; then
  goose session "$@" || printf '\ngoose exited (%s). 아래 셸에서 다시 실행하려면 goose session 을 입력하세요.\n\n' "$?"
else
  printf 'goose 명령을 찾을 수 없습니다. 이 이미지는 Goose 런타임용이 아닙니다.\n'
fi

exec bash -l
