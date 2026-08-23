#!/bin/sh
# Start the Orca runtime without a desktop session, and say when it is ready.
#
# Orca is an Electron application. `serve` is its supported headless mode: it
# starts Xvfb itself when DISPLAY is unset, and the AppImage is extracted at
# build time so no FUSE device is needed — which is what lets it run in a
# container at all.
#
# The ready line is a versioned single-line JSON contract, so this waits for it
# rather than sleeping and hoping. A runtime that answers late is a task that
# fails for a reason nobody can read.
set -eu

root="${ORCA_ROOT:-/opt/orca/squashfs-root}"
port="${ORCA_PORT:-6768}"
log="${ORCA_LOG:-/home/agent/.orca-serve.log}"

# The agents this fabric can start are pointed at the gateway before it is
# reachable, so a worker cannot be dispatched into a window where the config is
# not there yet.
if [ -x /usr/local/bin/agenthub-orca-agents-configure ]; then
  /usr/local/bin/agenthub-orca-agents-configure || true
fi

mkdir -p "$(dirname "$log")"
LIBGL_ALWAYS_SOFTWARE=1 "$root/AppRun" serve --port "$port" --json >"$log" 2>&1 &
runtime=$!

# Up to two minutes: Electron's first start on a cold home volume is slow, and
# every later start is fast. Failing early here would be failing on the one run
# that had to create everything.
waited=0
while [ "$waited" -lt 120 ]; do
  if grep -q '"type":"orca_server_ready"' "$log" 2>/dev/null; then
    echo "agenthub: orca runtime ready on port $port" >&2
    wait "$runtime"
    exit $?
  fi
  if ! kill -0 "$runtime" 2>/dev/null; then
    echo "agenthub: the orca runtime exited before it was ready" >&2
    tail -20 "$log" >&2
    exit 1
  fi
  sleep 1
  waited=$((waited + 1))
done
echo "agenthub: the orca runtime did not report ready within 120s" >&2
tail -20 "$log" >&2
kill "$runtime" 2>/dev/null || true
exit 1
