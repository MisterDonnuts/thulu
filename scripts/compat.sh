#!/usr/bin/env bash
# Each daemon gets its own docker:dind engine: the daemon's janitor and reconcilers act on whatever
# engine they reach,
# which on a developer's machine would be their real install.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${COMPAT_PREV_IMAGE:?set COMPAT_PREV_IMAGE to the previous release daemon image}"
: "${COMPAT_NEXT_IMAGE:?set COMPAT_NEXT_IMAGE to the daemon image built from this commit}"
PORT=${COMPAT_PORT:-18585}
KEEP=${COMPAT_KEEP:-0}
ARTIFACTS=${COMPAT_ARTIFACTS:-}

# Pinned by digest; PG_IMAGE must match the go-test service's postgres image.
PG_IMAGE=postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea
DIND_IMAGE=docker:27.5-dind@sha256:aa3df78ecf320f5fafdce71c659f1629e96e9de0968305fe1de670e0ca9176ce
READY_TIMEOUT=${COMPAT_READY_TIMEOUT:-120}

for tool in docker go openssl curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "compat: $tool is required" >&2; exit 2; }
done

run_id="octopus-compat-$$-$(openssl rand -hex 3)"
NET=$run_id
PG=$run_id-pg
DIND=$run_id-dind
DATA_VOL=$run_id-data
SOCK_VOL=$run_id-sock
TMP=$(mktemp -d)
chmod 700 "$TMP"
current=""

# SECRETBOX_KEY must stay the same across every daemon, or later steps cannot open what earlier ones
# sealed.
DEV_TOKEN=$(openssl rand -hex 32)
JWT_SECRET=$(openssl rand -hex 32)
SECRETBOX_KEY=$(openssl rand -hex 32)
PG_PASSWORD=$(openssl rand -hex 16)

log() { printf '[compat %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
  local rc=$?
  if [[ -n $ARTIFACTS ]]; then
    mkdir -p "$ARTIFACTS"
    cp "$TMP"/*.log "$ARTIFACTS"/ 2>/dev/null || true
    docker logs "$PG" >"$ARTIFACTS/postgres.log" 2>&1 || true
  fi
  if [[ $KEEP == 1 ]]; then
    log "COMPAT_KEEP=1: left $run_id-* containers/volumes and $TMP"
    exit "$rc"
  fi
  docker rm -f "$current" "$PG" "$DIND" >/dev/null 2>&1 || true
  docker volume rm "$DATA_VOL" "$SOCK_VOL" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$TMP"
  exit "$rc"
}
trap cleanup EXIT INT TERM

fail() {
  echo "compat: FAIL: $*" >&2
  exit 1
}

docker network create --internal=false "$NET" >/dev/null
docker volume create "$DATA_VOL" >/dev/null
docker volume create "$SOCK_VOL" >/dev/null

log "postgres ($PG_IMAGE)"
docker run -d --name "$PG" --network "$NET" \
  -e POSTGRES_USER=octopus -e POSTGRES_DB=octopus -e POSTGRES_PASSWORD="$PG_PASSWORD" \
  "$PG_IMAGE" >/dev/null

# --group=10001 plus --group-add 10001 on each daemon: images may run as different uids.
log "docker engine for the daemon ($DIND_IMAGE)"
docker run -d --privileged --name "$DIND" --network "$NET" \
  -v "$SOCK_VOL":/run/dind \
  "$DIND_IMAGE" dockerd --host=unix:///run/dind/docker.sock --group=10001 >/dev/null

for _ in $(seq 1 60); do
  docker exec "$PG" pg_isready -U octopus -d octopus >/dev/null 2>&1 \
    && docker exec "$DIND" docker -H unix:///run/dind/docker.sock version >/dev/null 2>&1 \
    && break
  sleep 1
done
docker exec "$PG" pg_isready -U octopus -d octopus >/dev/null 2>&1 || fail "postgres did not come up"
docker exec "$DIND" docker -H unix:///run/dind/docker.sock version >/dev/null 2>&1 \
  || { docker logs "$DIND" 2>&1 | tail -30; fail "the docker engine did not come up"; }

# Loopback only: dev mode refuses a wider bind.
start_daemon() {
  local image=$1 step=$2
  current=$run_id-$step
  docker run -d --name "$current" --network "$NET" \
    -p "127.0.0.1:$PORT:8585" --group-add 10001 \
    -v "$DATA_VOL":/data -v "$SOCK_VOL":/run/dind \
    -e OCTOPUS_DATABASE_URL="postgres://octopus:$PG_PASSWORD@$PG:5432/octopus?sslmode=disable" \
    -e OCTOPUS_DEV_MODE=1 -e OCTOPUS_DEV_MODE_CONTAINER=1 \
    -e OCTOPUS_DEV_TOKEN="$DEV_TOKEN" \
    -e OCTOPUS_JWT_SECRET="$JWT_SECRET" \
    -e OCTOPUS_SECRETBOX_KEY="$SECRETBOX_KEY" \
    -e DOCKER_HOST=unix:///run/dind/docker.sock \
    "$image" >/dev/null
  local deadline=$((SECONDS + READY_TIMEOUT))
  until curl -sf -o /dev/null --max-time 3 "http://127.0.0.1:$PORT/health/ready"; do
    if [[ $(docker inspect -f '{{.State.Running}}' "$current" 2>/dev/null) != true ]]; then
      docker logs "$current" 2>&1 | tail -60
      fail "$step: $image exited before it was ready"
    fi
    if ((SECONDS > deadline)); then
      docker logs "$current" 2>&1 | tail -60
      fail "$step: $image not ready after ${READY_TIMEOUT}s"
    fi
    sleep 1
  done
}

# pgx renders every server error as "(SQLSTATE xxxxx)"; Postgres logs each failed statement as
# "ERROR:".
stop_daemon() {
  local step=$1 since=$2
  docker stop -t 20 "$current" >/dev/null
  docker logs "$current" >"$TMP/daemon-$step.log" 2>&1
  docker rm "$current" >/dev/null
  current=""
  docker logs --since "$since" "$PG" >"$TMP/postgres-$step.log" 2>&1 || true
  local bad=0
  if grep -n 'SQLSTATE' "$TMP/daemon-$step.log" >&2; then bad=1; fi
  if grep -n 'ERROR:' "$TMP/postgres-$step.log" >&2; then bad=1; fi
  ((bad == 0)) || fail "$step: SQL errors (above)"
}

smoke() {
  local step=$1 pattern=$2
  COMPAT_URL="http://127.0.0.1:$PORT" \
    COMPAT_DEV_TOKEN="$DEV_TOKEN" \
    COMPAT_API_KEY_FILE="$TMP/bootstrap.json" \
    COMPAT_STATE="$TMP/state.json" \
    COMPAT_STEP="$step" \
    go test -count=1 -tags compat ./test/compat -run "$pattern" -v 2>&1 | tee "$TMP/smoke-$step.log"
  return "${PIPESTATUS[0]}"
}

run_step() {
  local step=$1 image=$2 pattern=$3 boot=${4:-}
  local since t0=$SECONDS
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  log "step $step: $image ($pattern)"
  start_daemon "$image" "$step"
  if [[ -n $boot ]]; then
    local ws
    ws=$(docker exec "$current" ./octopus workspace create \
      -name Compat -slug compat -owner-email compat@example.test | awk 'NR==1{print $1}')
    [[ -n $ws ]] || fail "$step: could not create the workspace"
    (umask 077 && docker exec "$current" ./octopus keys mint \
      -name compat-bootstrap -workspace "$ws" -role admin -permissions full \
      >"$TMP/bootstrap.json") || fail "$step: could not mint the bootstrap key"
  fi
  if ! smoke "$step" "$pattern"; then
    docker logs "$current" 2>&1 | tail -80 >&2
    fail "$step: smoke test failed against $image"
  fi
  stop_daemon "$step" "$since"
  log "step $step passed in $((SECONDS - t0))s"
}

start=$SECONDS
run_step seed "$COMPAT_PREV_IMAGE" '^TestSeed$' bootstrap
run_step next "$COMPAT_NEXT_IMAGE" '^(TestVerify|TestWrite)$'
run_step back "$COMPAT_PREV_IMAGE" '^(TestVerify|TestWrite)$'
run_step again "$COMPAT_NEXT_IMAGE" '^TestVerify$'
log "PASS: $COMPAT_PREV_IMAGE ⇄ $COMPAT_NEXT_IMAGE in $((SECONDS - start))s"
