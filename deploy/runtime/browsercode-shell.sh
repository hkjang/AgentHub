#!/bin/sh
# What a person sees when they open a BrowserCode runtime.
#
# The product is a terminal agent that drives a browser, so the browser session
# lands in its TUI. When it exits — the person quit it, or it could not start
# because the model binding is wrong — the session drops to a login shell instead
# of closing the tab, because a terminal that vanishes tells you nothing about
# why.
set -u

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

DO_NOT_TRACK=1
export DO_NOT_TRACK

if command -v bcode >/dev/null 2>&1; then
  bcode || printf '\nbcode exited (%s). 아래 셸에서 다시 실행하려면 bcode 를 입력하세요.\n\n' "$?"
else
  printf 'bcode 명령을 찾을 수 없습니다. 이 이미지는 BrowserCode 런타임용이 아닙니다.\n'
fi

exec bash -l
