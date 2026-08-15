#!/bin/sh
set -eu

case "${AGENTHUB_RUNTIME_TYPE:-opencode}" in
  opencode|qwencode)
    exec opencode serve --hostname 0.0.0.0 --port 4096
    ;;
  hermes|qwenpaw)
    mkdir -p "${HERMES_HOME:-/home/agent/.hermes}"
    if [ -f "${HERMES_CONFIG:-/etc/agenthub/hermes-config.yaml}" ]; then
      cp "${HERMES_CONFIG:-/etc/agenthub/hermes-config.yaml}" "${HERMES_HOME:-/home/agent/.hermes}/config.yaml"
    fi
    export API_SERVER_ENABLED=true
    export API_SERVER_HOST=0.0.0.0
    export API_SERVER_PORT=8642
    export API_SERVER_KEY="${API_SERVER_KEY:-${AGENTHUB_RUNTIME_TOKEN:?runtime token is required}}"
    exec /opt/hermes/.venv/bin/hermes gateway run --no-supervise
    ;;
  custom)
    if [ -z "${AGENTHUB_CUSTOM_COMMAND:-}" ]; then
      echo "AGENTHUB_CUSTOM_COMMAND is required for custom runtimes" >&2
      exit 64
    fi
    exec /bin/sh -lc "${AGENTHUB_CUSTOM_COMMAND}"
    ;;
  *)
    echo "Unsupported AGENTHUB_RUNTIME_TYPE: ${AGENTHUB_RUNTIME_TYPE}" >&2
    exit 64
    ;;
esac
