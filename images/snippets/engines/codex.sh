#!/usr/bin/env bash
# Flags and --json schema checked against codex-cli 0.142.0, the pin in
# images/kube-agent/Dockerfile.
# EFFORT is unwired because Capabilities() declares EffortFlag=false; enabling needs both -c
# model_reasoning_effort and that flag.

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

ENGINE_BIN=codex

# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/../engine-session.sh"

engine_auth_check() {
  if [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "ERROR: $(msg_en agent.errors.codex_no_auth)" >&2
    return 1
  fi
}

# codex authenticates from ~/.codex/auth.json, not OPENAI_API_KEY. The key digest catches a rotated
# key in a codex home that outlives its container.
# login reads the key from its own stdin so engine_run's stdin prompt stays intact for `codex exec
# -`.
codex_ensure_auth() {
  [ -n "${OPENAI_API_KEY:-}" ] || return 0
  local marker="${CODEX_HOME:-$HOME/.codex}/.octopus-key-sha256" sum
  sum=$(printenv OPENAI_API_KEY | sha256sum | cut -d' ' -f1)
  if [ "$(cat "$marker" 2>/dev/null)" = "$sum" ] && codex login status >/dev/null 2>&1; then
    return 0
  fi
  if printenv OPENAI_API_KEY | codex login --with-api-key >/dev/null 2>&1; then
    printf '%s' "$sum" >"$marker" 2>/dev/null || true
  fi
}

# codex has no SSE client, so SSE entries need mcp-remote.
codex_mcp_args() {
  [ -n "${OCTOPUS_MCP_SERVERS:-}" ] || return 0
  local gen="${OCTOPUS_ROOT}/snippets/mcp-codex-args.js"
  [ -f "$gen" ] || return 0
  if command -v mcp-remote >/dev/null 2>&1; then
    node "$gen"
  else
    echo "WARNING: $(msg_en agent.log.codex_no_mcp_remote)" >&2
    node "$gen" --no-mcp-remote
  fi
}

codex_quiet_args() {
  [ "${OCTOPUS_DISABLE_NONESSENTIAL_TRAFFIC:-}" = "1" ] || return 0
  printf '%s\n' -c analytics.enabled=false -c check_for_update_on_startup=false -c feedback.enabled=false
}

# codex-cli 0.142 ignores OPENAI_BASE_URL: the base URL only arrives through its config.
codex_base_url_args() {
  [ -n "${OPENAI_BASE_URL:-}" ] || return 0
  printf '%s\n' -c "openai_base_url=\"$OPENAI_BASE_URL\""
}

codex_home() { printf '%s\n' "${CODEX_HOME:-${HOME:-/home/node}/.codex}"; }

engine_skills_dir() { printf '%s/skills\n' "$(codex_home)"; }

codex_rollout() {
  find "$(codex_home)/sessions" -type f -name "rollout-*-$1.jsonl" 2>/dev/null | head -1
}

# `codex exec resume` needs only the rollout file (checked against codex-cli 0.142); never export
# auth.json, config.toml or sqlite state.
# The newest rollout is the run's: a run container starts with no sessions.
engine_session_save() {
  local dir="$1" root files f rel sid
  root="$(codex_home)/sessions"
  files=$(find "$root" -type f -name 'rollout-*.jsonl' 2>/dev/null)
  [ -n "$files" ] || return 0
  # shellcheck disable=SC2012,SC2086 # names carry no spaces; ls -t is the mtime sort
  f=$(ls -t $files | head -1)
  sid=$(basename "$f" .jsonl | sed -nE 's/.*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/\1/p')
  [ -n "$sid" ] || return 0
  rel=${f#"$root"/}
  mkdir -p "$dir/state/sessions/$(dirname "$rel")" || return 1
  cp "$f" "$dir/state/sessions/$rel" || return 1
  printf '%s\n' "$sid" >"$dir/id"
}

codex_session_restore() {
  local state="${SESSION_STATE_DIR:-}/state/sessions"
  if [ -z "$(codex_rollout "$SESSION_ID")" ] && [ -n "${SESSION_STATE_DIR:-}" ] && [ -d "$state" ]; then
    mkdir -p "$(codex_home)/sessions" && cp -R "$state/." "$(codex_home)/sessions/"
  fi
  [ -n "$(codex_rollout "$SESSION_ID")" ]
}

# A resume codex cannot do prints "no rollout found" on stderr and nothing on stdout.
codex_resume_failed() {
  case "$1" in
  '{"type":"error"'* | '{"type":"turn.failed"'*) return 0 ;;
  esac
  return 1
}

# --dangerously-bypass-approvals-and-sandbox: the run is unattended and the container is the
# boundary; codex's prompts and inner sandbox would deadlock it.
codex_cli() {
  local mcp_args=()
  mapfile -t mcp_args < <(codex_mcp_args)
  local quiet_args=()
  mapfile -t quiet_args < <(codex_quiet_args)
  local base_url_args=()
  mapfile -t base_url_args < <(codex_base_url_args)
  codex exec "$@" \
    --json \
    --skip-git-repo-check \
    --dangerously-bypass-approvals-and-sandbox \
    "${base_url_args[@]}" \
    "${quiet_args[@]}" \
    "${mcp_args[@]}" \
    -
}

# The resume subcommand rejects --model; it reuses the session's model.
codex_resume() { codex_cli resume "$SESSION_ID"; }
codex_fresh() { codex_cli --model "${MODEL:-gpt-5.4-mini}"; }

engine_run() {
  codex_ensure_auth
  # codex --json never names the model, and usage prices a run by it.
  ENGINE_SESSION_MODEL="${MODEL:-gpt-5.4-mini}" engine_session_run codex_resume_failed codex_session_restore codex_resume codex_fresh
}
