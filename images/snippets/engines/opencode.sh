#!/usr/bin/env bash
# Checked against opencode-ai 1.18.35, the pin in images/kube-agent/Dockerfile.
# OpenCode deep-merges many config layers (auth.json orgs, global and project dirs,
# OPENCODE_CONFIG*); oc_isolate and oc_env shut all but OPENCODE_CONFIG_CONTENT.

ENGINE_BIN=opencode

# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/../engine-session.sh"

OC_SNIPPETS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

engine_auth_check() {
  if [ -z "${LLM_API_KEY:-}" ]; then
    echo "ERROR: $(msg_en agent.errors.opencode_no_auth)" >&2
    return 1
  fi
}

oc_provider() { printf '%s\n' "${OCTOPUS_LLM_PROVIDER:-openai-compatible}"; }

oc_model() {
  if [ -n "${MODEL:-}" ]; then
    printf '%s\n' "$MODEL"
    return
  fi
  if [ "$(oc_provider)" = openai-compatible ]; then
    printf '%s\n' deepseek-chat
    return
  fi
  echo "ERROR: $(msg_en agent.errors.opencode_no_model provider "$(oc_provider)")" >&2
  return 1
}

engine_skills_dir() { printf '%s/.octopus/opencode-skills\n' "${HOME:-/home/node}"; }

# Leave the global dir read-only: writable, OpenCode writes opencode.jsonc and .gitignore and runs
# `npm install @opencode-ai/plugin` every run (opencode-ai 1.18.35).
oc_isolate() {
  local global="${XDG_CONFIG_HOME:-${HOME:-/home/node}/.config}/opencode" d f
  for d in "$global" "${HOME:-/home/node}/.opencode"; do
    [ -d "$d" ] || continue
    for f in "$d"/* "$d"/.[!.]*; do
      [ -e "$f" ] || continue
      case "$(basename "$f")" in
      .gitignore | node_modules | package.json | package-lock.json | bun.lock) continue ;;
      opencode.jsonc)
        # OpenCode writes this default itself; not a planted config.
        # shellcheck disable=SC2016 # "$schema" is the JSON key, not a variable
        [ "$(tr -d ' \n\t' <"$f")" = '{"$schema":"https://opencode.ai/config.json"}' ] && continue
        ;;
      esac
      echo "WARN: $(msg_en agent.log.opencode_config_removed path "$f")" >&2
    done
    chmod -R u+w "$d" 2>/dev/null
    rm -rf "$d"
  done
  mkdir -p "$global" && chmod 555 "$global"
  unset OPENCODE_CONFIG OPENCODE_CONFIG_DIR
}

# Nonessential-traffic switches duplicate the Go engine's NonessentialTrafficEnv so a hand-run
# adapter stays quiet.
oc_env() {
  OPENCODE_AUTH_CONTENT='{}' \
    OPENCODE_DISABLE_PROJECT_CONFIG=1 \
    OPENCODE_PURE=1 \
    OPENCODE_DISABLE_CLAUDE_CODE=1 \
    OPENCODE_DISABLE_EXTERNAL_SKILLS=1 \
    OPENCODE_DISABLE_AUTOUPDATE=1 \
    OPENCODE_DISABLE_MODELS_FETCH=1 \
    OPENCODE_DISABLE_LSP_DOWNLOAD=1 \
    OPENCODE_DISABLE_SHARE=1 \
    "$@"
}

oc_config() {
  node "$OC_SNIPPETS/opencode-config.js" "$1" "$(engine_skills_dir)" || {
    echo "ERROR: $(msg_en agent.errors.opencode_config_failed)" >&2
    return 1
  }
}

oc_data_dir() { printf '%s/opencode\n' "${XDG_DATA_HOME:-${HOME:-/home/node}/.local/share}"; }

# `opencode import` of the export restores full history and `run --session` continues it
# (opencode-ai 1.18.35); the newest session is the run's.
engine_session_save() {
  local dir="$1" sid
  [ -f "$(oc_data_dir)/opencode.db" ] || return 0
  sid=$(oc_env opencode session list --format json --max-count 1 </dev/null 2>/dev/null |
    sed -nE 's/.*"id": *"(ses_[A-Za-z0-9]+)".*/\1/p' | head -1)
  [ -n "$sid" ] || return 0
  mkdir -p "$dir/state" || return 1
  oc_env opencode export "$sid" </dev/null >"$dir/state/session.json" 2>/dev/null || {
    rm -f "$dir/state/session.json"
    return 1
  }
  printf '%s\n' "$sid" >"$dir/id"
}

oc_session_restore() {
  local f="${SESSION_STATE_DIR:-}/state/session.json"
  if [ -n "${SESSION_STATE_DIR:-}" ] && [ -s "$f" ]; then
    oc_env opencode import "$f" </dev/null >/dev/null 2>&1 && return 0
  fi
  [ -f "$(oc_data_dir)/opencode.db" ]
}

oc_resume_failed() {
  case "$1" in
  '{"type":"error"'*) return 0 ;;
  esac
  return 1
}

# --auto: without it a non-interactive run rejects every permission request; the container is the
# boundary.
# --title saves OpenCode a model call to title the session.
oc_cli() {
  local model config
  model=$(oc_model) || return 1
  config=$(oc_config "$model") || return 1
  oc_isolate
  OPENCODE_CONFIG_CONTENT="$config" oc_env \
    opencode run "$@" --format json --auto --title "${INSTANCE_ID:-octopus}" -m "$(oc_provider)/$model"
}

oc_resume() { oc_cli --session "$SESSION_ID"; }
oc_fresh() { oc_cli; }

engine_run() {
  local model
  model=$(oc_model) || return 1
  ENGINE_SESSION_MODEL="$model" engine_session_run oc_resume_failed oc_session_restore oc_resume oc_fresh
}
