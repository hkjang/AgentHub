#!/bin/sh
# Run one investigation and print its record.
#
# The platform reaches this agent by executing a command inside its container,
# and that command has no shell, no working directory and no profile. This
# wrapper supplies all three, and does one more thing that matters: it makes
# stdout carry the investigation's JSON and nothing else.
#
# Holmes renders its answer for a person as it works — headings, spinners, the
# analysis again at the end — and writes the machine-readable record only to the
# file named by --json-output-file. Parsing the rendered output would mean
# hunting for a JSON object inside prose, so the record goes to a file here and
# the file goes to stdout. What Holmes said for the person's benefit is dropped;
# it is the same analysis, and the record has it.
set -eu

cd "${AGENTHUB_WORKSPACE:-/workspace}" 2>/dev/null || cd "${HOME:-/home/agent}"

PATH="${HOME:-/home/agent}/.venv/bin:/opt/agenthub/venv/bin:${PATH}"
export PATH

record="$(mktemp "${TMPDIR:-/tmp}/agenthub-holmes-XXXXXX.json")"
errors="$(mktemp "${TMPDIR:-/tmp}/agenthub-holmes-XXXXXX.err")"
trap 'rm -f "$record" "$errors"' EXIT

# --no-interactive because nobody is at the keyboard; a prompt waiting for an
# answer would hang until the task's deadline. What Holmes rendered for a person
# goes nowhere and its complaints are kept, because the second of those is the
# only explanation there will be if no record appears.
status=0
holmes ask --no-interactive --json-output-file "$record" "$@" >/dev/null 2>"$errors" || status=$?

if [ -s "$record" ]; then
  cat "$record"
  # The record exists, so the investigation ran. A non-zero exit alongside it is
  # Holmes reporting something about the run rather than a failure to run, and
  # the caller reads the record for what happened.
  exit 0
fi

# No record: the run did not get far enough to produce one. The reason is
# whatever Holmes said while failing, and it is not run a second time to find
# out — a second investigation would spend a second investigation's tokens.
cat "$errors" >&2
exit "$([ "$status" -eq 0 ] && echo 1 || echo "$status")"
