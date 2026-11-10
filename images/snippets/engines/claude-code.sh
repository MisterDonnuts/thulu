#!/usr/bin/env bash
# Go counterpart: internal/engine/claudecode (Name "claude-code"); contract in ../engine-run.sh.

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

ENGINE_BIN=claude

# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/../engine-session.sh"

# Computed at top level: inside a function BASH_SOURCE may name the caller.
_CLAUDE_SNIPPETS_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

claude_config_dir() {
  printf '%s\n' "${CLAUDE_CONFIG_DIR:-${HOME:-/home/node}/.claude}"
}

claude_host_run() { [ "${OCTOPUS_HOST_RUN:-}" = "1" ]; }

# Do not repoint CLAUDE_CONFIG_DIR for host runs: on macOS the login lives in the Keychain under the
# config dir, and another dir means "Not logged in".
claude_plugin_dir() { printf '%s/.octopus-plugin\n' "$OCTOPUS_ROOT"; }

# A host run keeps credential files in its own temp dir: ~/.config is shared by every run on the
# machine, and two runs writing the same token file would swap identities.
claude_private_dir() {
  if claude_host_run; then
    printf '%s/.config/octopus\n' "${OCTOPUS_TMP:-${TMPDIR:-/tmp}}"
    return
  fi
  printf '%s/.config/octopus\n' "${HOME:-/tmp}"
}

# Wipe everything but the login so one task's transcript never reaches the next; safe because the
# daemon runs one of a person's runs at a time.
claude_personal_login_ready() {
  local dir="${CLAUDE_CONFIG_DIR:-}" f
  [ -n "$dir" ] && [ -f "$dir/.credentials.json" ] || return 1
  for f in "$dir"/* "$dir"/.[!.]* "$dir"/..?*; do
    [ -e "$f" ] || continue
    case "$(basename "$f")" in
    .credentials.json | .claude.json) ;;
    *) rm -rf -- "$f" ;;
    esac
  done
}

# Bedrock and Vertex may run on the workload's own identity, so the switch alone passes.
engine_auth_check() {
  # runner_login is the CLI's own stored login on the runner's machine (Keychain on macOS); Thulu
  # cannot see it to check it.
  if [ "${OCTOPUS_LLM_SOURCE:-}" = runner_login ]; then
    return 0
  fi
  if [ "${OCTOPUS_LLM_SOURCE:-}" = personal_login ]; then
    claude_personal_login_ready && return 0
    echo "ERROR: $(msg_en agent.errors.claude_no_personal_login)" >&2
    return 1
  fi
  if [ -n "${OCTOPUS_ANTHROPIC_IDENTITY_JWT:-}" ] && [ -n "${ANTHROPIC_FEDERATION_RULE_ID:-}" ]; then
    return 0
  fi
  if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ] &&
    [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] &&
    [ "${CLAUDE_CODE_USE_BEDROCK:-}" != "1" ] && [ "${CLAUDE_CODE_USE_VERTEX:-}" != "1" ]; then
    echo "ERROR: $(msg_en agent.errors.claude_no_auth)" >&2
    return 1
  fi
}

# The CLI treats a set-but-empty ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN as present, outranking
# federation, so empty ones are unset.
# The CLI rereads the token file on every exchange; the env copy is dropped so the agent's tools
# don't inherit it.
claude_auth_env() {
  local v
  # On runner_login no credential var may reach the CLI: any of them outranks the stored login.
  if [ "${OCTOPUS_LLM_SOURCE:-}" = runner_login ]; then
    unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL \
      OCTOPUS_ANTHROPIC_IDENTITY_JWT ANTHROPIC_IDENTITY_TOKEN ANTHROPIC_IDENTITY_TOKEN_FILE \
      ANTHROPIC_FEDERATION_RULE_ID ANTHROPIC_ORGANIZATION_ID ANTHROPIC_SERVICE_ACCOUNT_ID ANTHROPIC_WORKSPACE_ID
    return 0
  fi
  for v in ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN \
    ANTHROPIC_FEDERATION_RULE_ID ANTHROPIC_ORGANIZATION_ID ANTHROPIC_SERVICE_ACCOUNT_ID ANTHROPIC_WORKSPACE_ID; do
    [ -n "${!v:-}" ] || unset "$v"
  done
  [ -n "${OCTOPUS_ANTHROPIC_IDENTITY_JWT:-}" ] || return 0
  local dir
  dir=$(claude_private_dir)
  local file="$dir/anthropic-identity.jwt"
  (
    umask 077
    mkdir -p "$dir"
    printf '%s' "$OCTOPUS_ANTHROPIC_IDENTITY_JWT" >"$file"
  ) || {
    echo "ERROR: $(msg_en agent.errors.identity_token_write_failed file "$file")" >&2
    return 1
  }
  export ANTHROPIC_IDENTITY_TOKEN_FILE="$file"
  unset OCTOPUS_ANTHROPIC_IDENTITY_JWT ANTHROPIC_IDENTITY_TOKEN
}

# The Google SDK reads a file; the env copy is dropped so the agent's tools don't inherit it.
claude_vertex_key_file() {
  [ -n "${OCTOPUS_VERTEX_CREDENTIALS_B64:-}" ] || return 0
  local dir
  dir=$(claude_private_dir)
  local file="$dir/vertex-sa.json"
  (
    umask 077
    mkdir -p "$dir"
    printf '%s' "$OCTOPUS_VERTEX_CREDENTIALS_B64" | base64 -d >"$file"
  ) || {
    echo "ERROR: $(msg_en agent.errors.vertex_write_failed file "$file")" >&2
    return 1
  }
  export GOOGLE_APPLICATION_CREDENTIALS="$file"
  unset OCTOPUS_VERTEX_CREDENTIALS_B64
}

engine_skills_dir() {
  if claude_host_run; then
    printf '%s/skills\n' "$(claude_plugin_dir)"
    return
  fi
  printf '%s/skills\n' "$(claude_config_dir)"
}

# Claude Code's layout: <config dir>/projects/<cwd with every non-alphanumeric char turned into
# "-">.
claude_projects_dir() {
  printf '%s/projects/%s\n' "$(claude_config_dir)" "$(pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
}

claude_session_file() {
  local f
  for f in "$(claude_projects_dir)/$1.jsonl" "$(claude_config_dir)"/projects/*/"$1.jsonl"; do
    [ -f "$f" ] && { printf '%s\n' "$f"; return 0; }
  done
  return 1
}

# `claude --resume` needs only the transcript and its sibling dir (checked against Claude Code 2.1);
# never export credentials or settings.
# The newest transcript is the run's: a run container starts with empty projects and a resume
# appends to the restored file.
engine_session_save() {
  local dir="$1" f newest="" sid
  # A host run shares ~/.claude with the person and every other run: only its own project dir is its
  # session; the newest transcript elsewhere may be someone's open session.
  if claude_host_run; then
    set -- "$(claude_projects_dir)"/*.jsonl
  else
    set -- "$(claude_config_dir)"/projects/*/*.jsonl
  fi
  for f in "$@"; do
    case "$(basename "$f")" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]-*.jsonl) ;;
    *) continue ;;
    esac
    { [ -z "$newest" ] || [ "$f" -nt "$newest" ]; } && newest="$f"
  done
  [ -n "$newest" ] || return 0
  f="$newest"
  sid=$(basename "$f" .jsonl)
  mkdir -p "$dir/state" || return 1
  cp "$f" "$dir/state/" || return 1
  [ -d "${f%.jsonl}" ] && cp -R "${f%.jsonl}" "$dir/state/"
  printf '%s\n' "$sid" >"$dir/id"
}

# A transcript already on disk wins: in a warm chat container it is the newer copy.
claude_session_restore() {
  local state="${SESSION_STATE_DIR:-}/state" proj
  if ! claude_session_file "$SESSION_ID" >/dev/null && [ -n "${SESSION_STATE_DIR:-}" ] &&
    [ -f "$state/$SESSION_ID.jsonl" ]; then
    proj=$(claude_projects_dir)
    mkdir -p "$proj" &&
      cp "$state/$SESSION_ID.jsonl" "$proj/" &&
      { [ ! -d "$state/$SESSION_ID" ] || cp -R "$state/$SESSION_ID" "$proj/"; }
  fi
  claude_session_file "$SESSION_ID" >/dev/null
}

# Claude Code signals an impossible resume ("No conversation found") only by a first stdout line
# that is an error result with num_turns 0.
claude_resume_failed() {
  case "$1" in
  *'"type":"result"'*) ;;
  *) return 1 ;;
  esac
  case "$1" in *'"is_error":true'*) ;; *) return 1 ;; esac
  case "$1" in *'"num_turns":0,'* | *'"num_turns":0}'*) return 0 ;; esac
  return 1
}

claude_token_optimizer_settings() {
  [ "${OCTOPUS_TOKEN_OPTIMIZER:-}" = enforce ] || return 0
  local dir="${OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR:-${TMPDIR:-/tmp}/octopus-token-optimizer}"
  local hook="$_CLAUDE_SNIPPETS_DIR/token-optimizer-hook.js"
  local file="$dir/claude-settings.json"
  [ -f "$hook" ] || return 0
  mkdir -p "$dir" || return 0
  local read_hook bash_hook
  # i18n-ignore a settings file, not a message
  read_hook=$(printf '{"matcher":"Read","hooks":[{"type":"command","command":"node %s read","timeout":15}]}' "'$hook'")
  # i18n-ignore a settings file, not a message
  bash_hook=$(printf '{"matcher":"Bash","hooks":[{"type":"command","command":"node %s bash","timeout":15}]}' "'$hook'")
  # i18n-ignore a settings file, not a message
  printf '{"hooks":{"PreToolUse":[%s,%s]}}\n' "$read_hook" "$bash_hook" >"$file" || return 0
  printf '%s\n' "$file"
}

claude_host_args() {
  local plugin
  plugin=$(claude_plugin_dir)
  CLAUDE_HOST_ARGS=()
  if [ -d "$plugin/skills" ]; then
    mkdir -p "$plugin/.claude-plugin"
    # i18n-ignore a plugin manifest, not a message
    printf '{"name":"octopus-run","version":"1.0.0","description":"Skills granted to this Thulu run"}\n' \
      >"$plugin/.claude-plugin/plugin.json"
    CLAUDE_HOST_ARGS+=(--plugin-dir "$plugin")
  fi
  [ "${OCTOPUS_USER_SETTINGS:-}" = "1" ] || CLAUDE_HOST_ARGS+=(--setting-sources "")
  if [ "${AUDIT_ONLY:-}" != "1" ] && [ -n "${OCTOPUS_LOCAL_MCP_CONFIG:-}" ] && [ -f "$OCTOPUS_LOCAL_MCP_CONFIG" ]; then
    CLAUDE_HOST_ARGS+=(--mcp-config "$OCTOPUS_LOCAL_MCP_CONFIG")
  fi
}

# --strict-mcp-config: load only this run's --mcp-config, never a .mcp.json the repository brings or
# one in the user's settings.
claude_cli() {
  local effort_args="" mcp_args="${MCP_CONFIG_ARGS:-}" settings
  local -a settings_args=() host_args=()
  [ -n "${EFFORT:-}" ] && effort_args="--effort ${EFFORT}"
  # --settings adds to the user's settings instead of replacing them.
  settings=$(claude_token_optimizer_settings)
  [ -n "$settings" ] && settings_args=(--settings "$settings")
  if claude_host_run; then
    claude_host_args
    host_args=(${CLAUDE_HOST_ARGS[@]+"${CLAUDE_HOST_ARGS[@]}"})
  fi
  # AskUserQuestion needs a terminal and a run has none; agents ask through the octopus-api task_ask
  # tool.
  # shellcheck disable=SC2086 # flag strings, word-split on purpose
  set -- "$@" -p \
    --dangerously-skip-permissions \
    --disallowedTools AskUserQuestion \
    --strict-mcp-config \
    --max-turns "${MAX_TURNS:-20}" \
    --output-format stream-json \
    --verbose \
    $effort_args $mcp_args \
    ${settings_args[@]+"${settings_args[@]}"} \
    ${host_args[@]+"${host_args[@]}"}

  if [ "${STEER_ENABLED:-0}" = "1" ]; then
    local -a echo_args=()
    if [ -n "${RESUME_MESSAGE:-}" ]; then
      if [ -n "${ENGINE_ECHO_FILE:-}" ]; then
        echo_args=(--echo-initial-file "$ENGINE_ECHO_FILE")
      else
        echo_args=(--echo-initial)
      fi
    fi
    node "${OCTOPUS_ROOT}/snippets/steer-driver.js" \
      --queue "${STEER_QUEUE:-${OCTOPUS_ROOT}/.steer/queue.jsonl}" \
      --grace-ms "${STEER_GRACE_MS:-1500}" \
      ${echo_args[@]+"${echo_args[@]}"} \
      -- claude --input-format stream-json "$@"
    return
  fi
  claude "$@"
}

claude_resume() { claude_cli --resume "$SESSION_ID"; }
claude_fresh() { claude_cli --model "${MODEL:-sonnet}"; }

engine_run() {
  claude_auth_env || return 1
  claude_vertex_key_file || return 1
  engine_session_run claude_resume_failed claude_session_restore claude_resume claude_fresh
}
