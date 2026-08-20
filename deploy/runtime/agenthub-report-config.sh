#!/bin/sh
# Report what this Pod actually started with.
#
# A setting that silently did not apply is worse than one that was never offered:
# the operator believes their fleet is configured. So every initialiser ends here,
# reading back the file it just wrote and telling the control plane which keys are
# in it — never their values, because an overlay may carry an internal endpoint or
# a licence string.
#
# Best effort by design. A report that cannot be delivered must not stop a runtime
# from starting; the missing report is itself the signal, and the console shows the
# runtime as unverified rather than pretending.
set -eu

TARGET_FILE="${1:-}"
RUNTIME_TYPE="${AGENTHUB_RUNTIME_TYPE:-unknown}"
FINGERPRINT="${AGENTHUB_RUNTIME_SETTINGS_FINGERPRINT:-}"
CONTROL_PLANE="${AGENTHUB_CONTROL_PLANE_URL:-}"
RUNTIME_ID="${AGENTHUB_RUNTIME_ID:-}"
TOKEN="${AGENTHUB_RUNTIME_TOKEN:-}"
STATUS="${2:-applied}"

if [ -z "$CONTROL_PLANE" ] || [ -z "$RUNTIME_ID" ] || [ -z "$TOKEN" ]; then
  echo "agenthub: no control plane address, skipping the configuration report" >&2
  exit 0
fi

# The keys are read back out of the file that was written, not out of the intent:
# the point of the report is to describe what is on disk.
PAYLOAD=$(
  TARGET_FILE="$TARGET_FILE" RUNTIME_TYPE="$RUNTIME_TYPE" FINGERPRINT="$FINGERPRINT" \
  RUNTIME_ID="$RUNTIME_ID" STATUS="$STATUS" python3 - <<'PY'
import json, os, pathlib

target = os.environ.get("TARGET_FILE", "")
status = os.environ.get("STATUS", "applied")
keys, detail = [], ""
path = pathlib.Path(target) if target else None
if path and path.exists():
    text = path.read_text(encoding="utf-8", errors="replace")
    try:
        # JSON first, whatever the extension: the platform writes the Hermes
        # configuration as JSON, which is valid YAML and what Hermes reads. The line
        # scan is the fallback for a hand-edited YAML file, and it only needs the
        # top-level keys — enough to report without adding a parser to the image.
        try:
            document = json.loads(text)
            keys = sorted(f"config:{k}" for k in document) if isinstance(document, dict) else []
        except json.JSONDecodeError:
            keys = sorted({f"config:{line.split(':', 1)[0].strip()}" for line in text.splitlines()
                           if line and line[0] not in " #-" and ":" in line})
    except Exception as error:  # noqa: BLE001 - the report must not fail the start
        status, detail = "unreadable", str(error)[:200]
elif target:
    status, detail = "missing", f"{target} was not written"

# The variables an administrator can set are reported by name, never by value.
# AGENTHUB_REPORT_ENV_KEYS carries the names this runtime's overlay declared, so a
# runtime configured entirely through the environment — Langflow has no
# configuration file at all — can be checked against what was intended rather
# than only against a fingerprint it echoes back.
declared = {name.strip() for name in os.environ.get("AGENTHUB_REPORT_ENV_KEYS", "").split(",") if name.strip()}
wellknown = {"LANG", "LC_ALL", "TZ", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "NODE_OPTIONS"}
for name in sorted(declared | wellknown):
    if name in os.environ:
        keys.append(f"env:{name}")
# A declared variable missing from this container's environment is the failure the
# report exists to surface, so it is named rather than quietly omitted.
missing = sorted(name for name in declared if name not in os.environ)
if missing:
    keys.extend(f"env-missing:{name}" for name in missing)
    if status == "applied":
        status, detail = "incomplete", "주입되지 않은 환경변수: " + ", ".join(missing[:8])

print(json.dumps({
    "runtimeId": os.environ["RUNTIME_ID"],
    "runtimeType": os.environ.get("RUNTIME_TYPE", ""),
    "fingerprint": os.environ.get("FINGERPRINT", ""),
    "file": target,
    "status": status,
    "detail": detail,
    "keys": keys,
}, ensure_ascii=False))
PY
)

# 5 seconds: a slow control plane must not delay a Pod's start. curl is present in
# the images the platform builds, but a runtime image is allowed to be somebody
# else's; python3 is already required above, so it is the fallback rather than a
# report that is silently skipped.
REPORT_URL="${CONTROL_PLANE%/}/api/v1/runtime-gateway/config-report"
if command -v curl >/dev/null 2>&1; then
  curl -sS -m 5 -X POST "$REPORT_URL" \
    -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
    -d "$PAYLOAD" >/dev/null 2>&1 || echo "agenthub: configuration report could not be delivered" >&2
else
  REPORT_URL="$REPORT_URL" TOKEN="$TOKEN" PAYLOAD="$PAYLOAD" python3 - <<'POST' >/dev/null 2>&1 || echo "agenthub: configuration report could not be delivered" >&2
import os, urllib.request
request = urllib.request.Request(os.environ["REPORT_URL"], data=os.environ["PAYLOAD"].encode(),
                                 headers={"Authorization": "Bearer " + os.environ["TOKEN"],
                                          "Content-Type": "application/json"}, method="POST")
urllib.request.urlopen(request, timeout=5).read()
POST
fi
echo "agenthub-config-report $PAYLOAD" >&2
