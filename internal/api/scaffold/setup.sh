#!/usr/bin/env bash
# $ENGINE is injected by the daemon from the thulu.engine compose label; each engine needs
# engines/<name>.sh.
set -euo pipefail

# /engines/ is mounted from ./engines/ by docker-compose.yml.
ENGINE="${ENGINE:-claude-code}"
RUNNER="/engines/$ENGINE.sh"

echo "[agent] starting (engine: $ENGINE)..."

if [ ! -f "$RUNNER" ]; then
  echo "[agent] ERROR: no runner for engine '$ENGINE' at $RUNNER" >&2
  echo "[agent]        add engines/$ENGINE.sh or fix the thulu.engine label." >&2
  exit 1
fi

# shellcheck source=/dev/null
"$RUNNER"

echo "[agent] done."
