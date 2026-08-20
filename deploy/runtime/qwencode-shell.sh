#!/bin/sh
# What a person sees when they open a Qwen Code runtime.
#
# The product is a terminal UI, so the browser session lands directly in it
# rather than at a shell prompt with instructions. When the agent exits — the
# person quit it, or it could not start because the model binding is wrong — the
# session drops to a login shell instead of closing the tab, because a terminal
# that vanishes tells you nothing about why.
set -u

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

if command -v qwen >/dev/null 2>&1; then
  qwen "$@" || printf '\nqwen exited (%s). 아래 셸에서 다시 실행하려면 qwen 을 입력하세요.\n\n' "$?"
else
  printf 'qwen 명령을 찾을 수 없습니다. 이 이미지는 Qwen Code 런타임용이 아닙니다.\n'
fi

exec bash -l
