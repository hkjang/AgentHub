#!/bin/sh
# Start the two programs this runtime needs, and keep the browser alive.
#
# The browser is not a nicety here: it is the reason this runtime exists, and the
# agent's only tool for reaching a page. Starting it with `&` and hoping was not
# good enough for three reasons, all of which look identical from outside — a
# runtime stuck on "starting".
#
# Its output went to a file inside the container, so a browser that refused to
# start explained itself to nobody. It is on stderr now, which is the container's
# log, which is what the console's log viewer shows.
#
# Nothing watched it. A browser that died an hour in left a terminal that still
# answered and an agent whose every tool call failed.
#
# And nothing bounded the wait. A container that never becomes ready is reported
# as starting for as long as somebody is willing to look at it; this one gives up
# and exits, which restarts the Pod and puts the reason in its logs.
set -eu

PROFILE="${AGENTHUB_BROWSER_PROFILE:-/home/agent/.chrome-profile}"
PORT="${AGENTHUB_BROWSER_DEBUG_PORT:-9222}"
READY_TIMEOUT="${AGENTHUB_BROWSER_READY_TIMEOUT:-90}"

mkdir -p "$PROFILE"

# The browser lives inside this loop, which owns it: it starts it, waits on it,
# and starts it again when it goes.
#
# Waiting rather than polling is the point. `kill -0` was the obvious way and it
# is wrong — a dead child nobody has reaped is a zombie, its process entry still
# exists, and `kill -0` calls that alive. The first version of this watched a
# browser that had been dead for an hour. Waiting reaps the child and returns the
# moment it dies, which is both the notification and the cleanup.
#
# --no-sandbox because Chromium's own sandbox needs user namespaces the
# restricted security profile denies; the Pod is the boundary instead.
# --disable-dev-shm-usage because a Pod's /dev/shm is 64MB by default and a
# browser that runs out of it dies in the middle of a page rather than at start.
supervise_browser() {
  while true; do
    chromium --headless --no-sandbox --disable-dev-shm-usage \
      --remote-debugging-address=127.0.0.1 --remote-debugging-port="$PORT" \
      --user-data-dir="$PROFILE" about:blank >&2 &
    browser=$!
    wait "$browser" || true
    echo "agenthub: the browser exited; restarting it" >&2
    sleep 2
  done
}

supervise_browser &
supervisor=$!

# Wait for it to answer before the terminal comes up, so a browser that cannot
# start is discovered here rather than by the agent's first tool call.
waited=0
until curl -fsS -m 2 "http://127.0.0.1:${PORT}/json/version" >/dev/null 2>&1; do
  waited=$((waited + 1))
  if [ "$waited" -ge "$READY_TIMEOUT" ]; then
    echo "agenthub: the browser did not answer on port ${PORT} within ${READY_TIMEOUT}s; its output is above" >&2
    kill "$supervisor" 2>/dev/null || true
    exit 1
  fi
  sleep 1
done
echo "agenthub: browser ready on 127.0.0.1:${PORT} after ${waited}s" >&2

# The terminal runs as a child too, so this script stays the process the
# container is built around: it reaps what the browser leaves behind, and it
# stops when the terminal stops.
"$@" &
terminal=$!
wait "$terminal"
status=$?
kill "$supervisor" 2>/dev/null || true
exit "$status"
