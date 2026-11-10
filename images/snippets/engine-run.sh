#!/usr/bin/env bash
# Adapter contract (engines/<engine>.sh) and its Go half: docs/engines.md;
# internal/engine/conformance_test.go checks both.

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

set -eo pipefail

# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/i18n.sh"

_engine_error() { jq -cn --arg e "$1" '{type: "error", error: $e}' >&2; }

ENGINE="${ENGINE:-claude-code}"
ADAPTER="${OCTOPUS_ROOT}/snippets/engines/${ENGINE}.sh"

if [ ! -f "$ADAPTER" ]; then
  _engine_error "$(msg_en agent.errors.engine_no_adapter engine "$ENGINE" path "$ADAPTER")"
  exit 3
fi

# shellcheck source=/dev/null
source "$ADAPTER"

if ! declare -f engine_run >/dev/null 2>&1; then
  _engine_error "$(msg_en agent.errors.adapter_no_engine_run path "$ADAPTER")"
  exit 3
fi

if [ -n "${ENGINE_BIN:-}" ] && ! command -v "$ENGINE_BIN" >/dev/null 2>&1; then
  _engine_error "$(msg_en agent.errors.engine_cli_missing engine "$ENGINE" bin "$ENGINE_BIN")"
  exit 3
fi

# The daemon's collector reads the octopus.token_optimizer line from the log;
# token-optimizer-hook.js reads the failures file.
# The worker host logs to a file, never stdout/stderr, so a host that outlives this script cannot
# hold the engine's pipes open.
_TOKOPT_HOST_DIR=""
_TOKOPT_HOST_PID=""

_tokopt_host_stop() {
  [ -n "$_TOKOPT_HOST_DIR" ] || return 0
  if [ -n "$_TOKOPT_HOST_PID" ]; then
    kill "$_TOKOPT_HOST_PID" 2>/dev/null || true
    wait "$_TOKOPT_HOST_PID" 2>/dev/null || true
  fi
  [ -s "$_TOKOPT_HOST_DIR/host.log" ] && cat "$_TOKOPT_HOST_DIR/host.log" >&2
  rm -rf "$_TOKOPT_HOST_DIR"
  _TOKOPT_HOST_DIR=""
  _TOKOPT_HOST_PID=""
}

_tokopt_host_start() {
  local dir sock tries=0
  dir=$(mktemp -d "${TMPDIR:-/tmp}/octopus-tokopt.XXXXXX") || return 0
  chmod 700 "$dir"
  sock="$dir/worker.sock"
  _TOKOPT_HOST_DIR="$dir"
  trap _tokopt_host_stop EXIT
  node "$(dirname "${BASH_SOURCE[0]}")/token-optimizer-worker-host.js" "$sock" "$$" \
    </dev/null >/dev/null 2>"$dir/host.log" &
  _TOKOPT_HOST_PID=$!
  while [ ! -S "$sock" ] && [ "$tries" -lt 50 ] && kill -0 "$_TOKOPT_HOST_PID" 2>/dev/null; do
    sleep 0.1
    tries=$((tries + 1))
  done
  [ -S "$sock" ] || echo "WARN: $(msg_en agent.log.tokopt_host_not_ready socket "$sock")" >&2
  # Exported even when not ready, so octopus-delegate fails as unreachable instead of starting a
  # claude that is not logged in.
  export OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET="$sock"
}

# A socket inherited from an outer run is not this run's host.
unset OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET
case "${OCTOPUS_TOKEN_OPTIMIZER:-}" in
suggest | enforce)
  _tokopt_bytes="${OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES:-}"
  case "$_tokopt_bytes" in '' | *[!0-9]*) _tokopt_bytes=24576 ;; esac
  _tokopt_bytes=$((10#$_tokopt_bytes))
  [ "$_tokopt_bytes" -gt 0 ] || _tokopt_bytes=24576
  export OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES="$_tokopt_bytes"
  PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bin:$PATH"
  export PATH
  rm -f "${OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR:-${TMPDIR:-/tmp}/octopus-token-optimizer}/failures"
  # i18n-ignore a stream-json event, not a message
  printf '{"type":"octopus.token_optimizer","level":"%s","threshold_bytes":%d}\n' \
    "$OCTOPUS_TOKEN_OPTIMIZER" "$_tokopt_bytes"
  if [ "${OCTOPUS_TOKEN_OPTIMIZER_READ_VIA:-}" = cli ]; then
    if [ -n "${OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN:-}" ]; then
      _tokopt_host_start
    else
      echo "WARN: $(msg_en agent.log.tokopt_worker_no_token)" >&2
      export OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET=none
    fi
  fi
  ;;
*)
  unset OCTOPUS_TOKEN_OPTIMIZER OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES
  ;;
esac
# The worker's token stays with the host: the engine, the agent's tools and their children must
# never see it.
unset OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN

# Not exec'd: the EXIT trap stops the worker host once the engine is done.
engine_run
