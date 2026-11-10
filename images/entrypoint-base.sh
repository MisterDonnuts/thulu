#!/usr/bin/env bash
# A runner also executes these scripts on its own host, with OCTOPUS_ROOT and OCTOPUS_TMP set to a
# per-run dir: never hardcode /workspace or /tmp.
: "${OCTOPUS_ROOT:=/workspace}"
: "${OCTOPUS_TMP:=/tmp}"
export OCTOPUS_ROOT OCTOPUS_TMP

set -euo pipefail

AGENT_NAME="${AGENT_NAME:-agent}"
MAX_TURNS="${MAX_TURNS:-20}"
MODEL="${MODEL:-}"

# Messages: msg (the daemon's language) and msg_en (logs). See snippets/i18n.sh.
# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/i18n.sh"

# Docker mode starts as root: the prologue does only root work, then the script re-execs as node via
# gosu, so nothing from an agent
# definition or repo runs as root. Kubernetes mode starts unprivileged and skips the prologue
# (fsGroup handles volumes).
# Must run before the privilege drop: as root the CA goes into the system store; exported variables
# survive the re-exec.

# A host run (OCTOPUS_HOST_RUN=1) runs as an ordinary user on a machine Thulu does not own:
# skip the privilege drop, the system CA store and resolv.conf, and leave the host's trust and DNS
# alone.
_octopus_host_run=""
[ "${OCTOPUS_HOST_RUN:-}" = "1" ] && _octopus_host_run=1

if [ -z "$_octopus_host_run" ] && [ -z "${OCTOPUS_PRIVS_DROPPED:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/ca-trust.sh" ]; then
  # shellcheck source=/dev/null
  source "${OCTOPUS_ROOT}/snippets/ca-trust.sh"
fi

_octopus_root_prologue() {
  # /workspace is chowned non-recursively so its read-only assets stay root's.
  mkdir -p "${OCTOPUS_ROOT}/runs"
  chown node:node "${OCTOPUS_ROOT}" 2>/dev/null || true
  chown -R node:node "${OCTOPUS_ROOT}/runs"
  if [ -d "${OCTOPUS_ROOT}/project" ]; then
    chown -R node:node "${OCTOPUS_ROOT}/project" 2>/dev/null || chmod -R u+rwX,g+rwX "${OCTOPUS_ROOT}/project" 2>/dev/null || true
  fi
  if [ -d "${OCTOPUS_ROOT}/projects" ]; then
    chown -R node:node "${OCTOPUS_ROOT}/projects" 2>/dev/null || chmod -R u+rwX,g+rwX "${OCTOPUS_ROOT}/projects" 2>/dev/null || true
  fi
  if [ "${STAGE:-}" = "chat" ]; then
    mkdir -p /home/node/.claude
    chown -R node:node /home/node/.claude 2>/dev/null || true
  fi
  # The engine volume Docker mounts for the workspace (internal/enginecache): node installs the
  # engines there on first use. Top level only: what is inside node already owns.
  if [ -n "${OCTOPUS_ENGINE_DIR:-}" ] && [ -d "$OCTOPUS_ENGINE_DIR" ]; then
    chown node:node "$OCTOPUS_ENGINE_DIR" 2>/dev/null || true
  fi
  if [ "${OCTOPUS_LLM_SOURCE:-}" = personal_login ] && [ -d "${CLAUDE_CONFIG_DIR:-}" ]; then
    chown -R node:node "$CLAUDE_CONFIG_DIR" 2>/dev/null || true
  fi

  # Access goes through the socket's group, never a world-writable socket.
  if [ -S /var/run/docker.sock ]; then
    local sock_gid sock_group
    sock_gid=$(stat -c %g /var/run/docker.sock 2>/dev/null || echo "")
    if [ -n "$sock_gid" ]; then
      sock_group=$(getent group "$sock_gid" | cut -d: -f1 || true)
      if [ -z "$sock_group" ]; then
        groupadd -g "$sock_gid" octopus-docker 2>/dev/null && sock_group=octopus-docker
      fi
      # gid 0 (Docker Desktop) joins the root group; the operator accepted that with
      # OCTOPUS_ALLOW_AGENT_DOCKER_SOCKET.
      if [ -n "$sock_group" ] && usermod -aG "$sock_group" node 2>/dev/null; then
        msg_en agent.log.docker_socket_joined group "$sock_group" gid "$sock_gid"
      else
        echo "WARN: $(msg_en agent.log.docker_socket_denied gid "$sock_gid")" >&2
      fi
    fi
  fi

  # Append, never overwrite: Docker's embedded resolver (container names, host.docker.internal) must
  # stay first.
  if [ -n "${OCTOPUS_FALLBACK_DNS:-}" ]; then
    local ns
    for ns in ${OCTOPUS_FALLBACK_DNS//,/ }; do
      case "$ns" in
        *[!0-9a-fA-F:.]*) echo "WARN: $(msg_en agent.log.dns_not_ip entry "$ns")" >&2 ;;
        # i18n-ignore a resolv.conf line, not a message
        *) echo "nameserver $ns" >> /etc/resolv.conf ;;
      esac
    done
  fi
}

if [ -z "$_octopus_host_run" ] && [ "$(id -u)" = "0" ]; then
  if [ -n "${OCTOPUS_PRIVS_DROPPED:-}" ]; then
    echo "ERROR: $(msg_en agent.errors.still_root)" >&2
    exit 1
  fi
  _octopus_root_prologue
  if ! command -v gosu >/dev/null 2>&1; then
    echo "ERROR: $(msg_en agent.errors.no_gosu)" >&2
    exit 1
  fi
  exec env HOME=/home/node OCTOPUS_PRIVS_DROPPED=1 gosu node bash "${BASH_SOURCE[0]}" "$@"
fi

if [ -z "$_octopus_host_run" ] && [ -n "${OCTOPUS_FALLBACK_DNS:-}" ] && [ -z "${OCTOPUS_PRIVS_DROPPED:-}" ]; then
  echo "WARN: $(msg_en agent.log.dns_needs_root)" >&2
fi

CONTINUE_FILE="${OCTOPUS_ROOT}/runs/.continue"
if [ -f "$CONTINUE_FILE" ]; then
  echo "CONTINUE: $(msg_en agent.log.resuming_run)"
  source "$CONTINUE_FILE"
  rm -f "$CONTINUE_FILE"
fi

INSTANCE_ID="${INSTANCE_ID:-${AGENT_NAME}-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "${OCTOPUS_ROOT}/runs"
echo "$INSTANCE_ID" > "${OCTOPUS_ROOT}/runs/.instance_id"

echo "============================================"
echo "  $AGENT_NAME ($INSTANCE_ID)"
echo "============================================"

ENGINE="${ENGINE:-claude-code}"
ENGINE_ADAPTER="${OCTOPUS_ROOT}/snippets/engines/${ENGINE}.sh"
if [ ! -f "$ENGINE_ADAPTER" ]; then
  echo "ERROR: $(msg_en agent.errors.engine_no_adapter engine "$ENGINE" path "$ENGINE_ADAPTER")"
  exit 1
fi
# shellcheck source=/dev/null
source "$ENGINE_ADAPTER"

if declare -f engine_auth_check >/dev/null 2>&1; then
  engine_auth_check || exit 1
fi

OUTPUT_DIR="${OCTOPUS_ROOT}/runs/$INSTANCE_ID"
mkdir -p "$OUTPUT_DIR"
# Prompts name this short path instead: a model copying the instance id by hand has written to
# ..._in_progress-... when the id says -in_progress-, and those files never reach the daemon.
PROMPT_OUTPUT_DIR="$OUTPUT_DIR"
if ln -sfn "$OUTPUT_DIR" "${OCTOPUS_ROOT}/output" 2>/dev/null; then
  PROMPT_OUTPUT_DIR="${OCTOPUS_ROOT}/output"
fi
# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/post-results.sh"
# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/session-snapshot.sh"

if [ "${STAGE:-}" = "chat" ]; then
  MCP_CONFIG_ARGS=""
  if [ -n "${MCP_TOOLS:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/mcp-config.sh" ]; then
    source "${OCTOPUS_ROOT}/snippets/mcp-config.sh" || true
  fi
  echo "$MCP_CONFIG_ARGS" > "${OCTOPUS_ROOT}/.mcp-args"

  echo "============================================"
  msg_en agent.log.chat_banner agent "$AGENT_NAME" chat "${CHAT_ID:-?}" model "${MODEL:-$(msg_en agent.log.default)}" \
    tools "${MCP_TOOLS:-$(msg_en agent.log.none)}" args "$MCP_CONFIG_ARGS"
  echo "============================================"
  if [ -n "${OCTOPUS_EXEC_TOKEN:-}" ] && command -v sandbox-execagent >/dev/null 2>&1; then
    msg_en agent.log.chat_serving_execagent addr "${OCTOPUS_EXEC_ADDR:-:8587}"
    exec sandbox-execagent
  fi
  msg_en agent.log.chat_waiting
  exec tail -f /dev/null
fi

if [ "${AUDIT_ONLY:-}" = "1" ]; then
  set +eu
  EXIT_CODE="${AUDITED_EXIT:-0}"
  # shellcheck source=/dev/null
  source "${OCTOPUS_ROOT}/snippets/auditor.sh"
  _thulu_post_auditor_feedback
  _thulu_post_comment "$OUTPUT_DIR/auditor-comment.md"
  # A verdict counts whatever the CLI exited with; without one the task should show the real code.
  [ -f "$OUTPUT_DIR/feedback.md" ] && exit 0
  exit "${AUDITOR_EXIT:-0}"
fi

PROMPT_FILE=""
AGENT_EXPORTS=""
MCP_CONFIG_ARGS=""

if [ -f "${OCTOPUS_ROOT}/setup.sh" ]; then
  source "${OCTOPUS_ROOT}/setup.sh"
elif [ -f "${OCTOPUS_ROOT}/instructions/prompts/main.md" ] || [ -f "${OCTOPUS_ROOT}/instructions/prompts/${STAGE:-}.md" ]; then
  MAIN_PROMPT="${OCTOPUS_ROOT}/instructions/prompts/main.md"
  STAGE_PROMPT="${OCTOPUS_ROOT}/instructions/prompts/${STAGE:-}.md"

  MERGED="${OCTOPUS_TMP}/merged-prompt.md"
  : > "$MERGED"
  [ -f "$MAIN_PROMPT" ] && cat "$MAIN_PROMPT" > "$MERGED"

  if [ -n "${STAGE:-}" ] && [ -f "$STAGE_PROMPT" ]; then
    echo "PROMPT: $(msg_en agent.log.prompt_stage_appended path "$STAGE_PROMPT")"
    [ -s "$MERGED" ] && printf '\n\n---\n\n' >> "$MERGED"
    cat "$STAGE_PROMPT" >> "$MERGED"
  fi

  PROMPT_FILE="$MERGED"

  source "${OCTOPUS_ROOT}/snippets/context.sh"
  if [ "${SKILLS_MODE:-inline}" = "native" ] && [ -f "${OCTOPUS_ROOT}/snippets/skills-native.sh" ]; then
    source "${OCTOPUS_ROOT}/snippets/skills-native.sh"
  fi
  source "${OCTOPUS_ROOT}/snippets/skills.sh"

  OUTPUT_SUBDIRS="$STAGE"
  source "${OCTOPUS_ROOT}/snippets/output-dirs.sh"
else
  PROMPT_FILE="${OCTOPUS_ROOT}/instructions/prompt.md"
fi

if [ -n "${MCP_TOOLS:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/mcp-config.sh" ]; then
  source "${OCTOPUS_ROOT}/snippets/mcp-config.sh" || true
fi

if [ -n "${MCP_TOOLS:-}" ] && [ -n "${MERGED:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/mcp-skills.sh" ]; then
  source "${OCTOPUS_ROOT}/snippets/mcp-skills.sh" || true
fi

source "${OCTOPUS_ROOT}/snippets/git-native.sh" || true

if [ -n "${TASK_ID:-}" ] && [ -n "${MERGED:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/task-brief.sh" ]; then
  source "${OCTOPUS_ROOT}/snippets/task-brief.sh" || true
fi

if [ -n "${STAGE:-}" ] && [ -n "${MERGED:-}" ] && [ -f "${OCTOPUS_ROOT}/snippets/project-stage.sh" ]; then
  source "${OCTOPUS_ROOT}/snippets/project-stage.sh" || true
fi

if [ -n "${_octopus_host_run:-}" ] && [ "$OCTOPUS_ROOT" != "/workspace" ] && [ -n "${MERGED:-}" ] && [ -f "$MERGED" ]; then
  _octopus_prompt=$(cat "$MERGED")
  printf '%s\n' "${_octopus_prompt//\/workspace\//${OCTOPUS_ROOT}/}" > "$MERGED"
  unset _octopus_prompt
fi

if [ -z "$PROMPT_FILE" ]; then
  echo "ERROR: $(msg_en agent.errors.no_prompt_file)"
  exit 1
fi

if [ ! -f "$PROMPT_FILE" ]; then
  echo "ERROR: $(msg_en agent.errors.prompt_not_found path "$PROMPT_FILE")"
  exit 1
fi

msg_en agent.log.run_info engine "$ENGINE" instance "$INSTANCE_ID" model "${MODEL:-$(msg_en agent.log.default)}" \
  effort "${EFFORT:-$(msg_en agent.log.default)}" max_turns "$MAX_TURNS" \
  prompt "$PROMPT_FILE" output "$OUTPUT_DIR"
echo ""

# max_turns exits 1 and the session_id must still be saved.
set +eo pipefail

RESUME_BASE_PROMPT=""
if [ -n "${SESSION_ID:-}" ] || [ -n "${RESUME_MESSAGE:-}" ]; then
  RESUME_BASE_PROMPT="$PROMPT_FILE"
  _octopus_session_fetch
  RESUME_FILE="$OUTPUT_DIR/.resume-message.md"
  if [ -n "${RESUME_MESSAGE:-}" ]; then
    printf '%s\n' "$RESUME_MESSAGE" > "$RESUME_FILE"
  else
    msg agent.prompts.resume_more_turns turns "$MAX_TURNS" > "$RESUME_FILE"
  fi
  chown node:node "$RESUME_FILE" 2>/dev/null || true
  PROMPT_FILE="$RESUME_FILE"
  if [ -n "${SESSION_ID:-}" ]; then
    echo "RESUME: $(msg_en agent.log.resuming_session session "$SESSION_ID")"
  else
    echo "RESUME: $(msg_en agent.log.resuming_without_session)"
  fi
fi

# The daemon reads .thulu-prompt.md for the agent_prompt timeline event; keep the name.
cp "$PROMPT_FILE" "$OUTPUT_DIR/.thulu-prompt.md" 2>/dev/null || true

# Created before the engine starts so steer-driver.js never watches a missing file; the daemon
# appends to it via docker exec.
STEER_QUEUE="${STEER_QUEUE:-${OCTOPUS_ROOT}/.steer/queue.jsonl}"
if [ "${STEER_ENABLED:-0}" = "1" ]; then
  mkdir -p "$(dirname "$STEER_QUEUE")"
  : > "$STEER_QUEUE"
  chown -R node:node "$(dirname "$STEER_QUEUE")" 2>/dev/null || true
  chmod 666 "$STEER_QUEUE" 2>/dev/null || true
  msg_en agent.log.steering_on queue "$STEER_QUEUE"
fi

# i18n-ignore a shell script, not a message
bash -c "
  export HOME=\${HOME:-/home/node}
  $AGENT_EXPORTS
  export ENGINE='$ENGINE' MODEL='$MODEL' MAX_TURNS='$MAX_TURNS' EFFORT='${EFFORT:-}' MCP_CONFIG_ARGS='${MCP_CONFIG_ARGS:-}' SESSION_ID='${SESSION_ID:-}'
  export SESSION_STATE_DIR='$SESSION_STATE_DIR' CONVERSATION_FILE='$CONVERSATION_FILE' RESUME_BASE_PROMPT='$RESUME_BASE_PROMPT'
  export STEER_ENABLED='${STEER_ENABLED:-0}' STEER_QUEUE='$STEER_QUEUE' STEER_GRACE_MS='${STEER_GRACE_MS:-1500}'
  cat '$PROMPT_FILE' | '${OCTOPUS_ROOT}/snippets/engine-run.sh'
" 2>&1 | tee -a "$OUTPUT_DIR/session.log"

EXIT_CODE=${PIPESTATUS[0]}

# Seal the queue so a message arriving during upload fails in the daemon (which resumes the stage)
# instead of being lost.
# steer-driver.js seals it itself; this covers a driver that died.
if [ "${STEER_ENABLED:-0}" = "1" ]; then
  : > "$(dirname "$STEER_QUEUE")/closed" 2>/dev/null || true
fi

source "${OCTOPUS_ROOT}/snippets/result-push.sh" || true

_octopus_session_upload

set -eo pipefail

echo ""
echo "============================================"
msg_en agent.log.run_finished instance "$INSTANCE_ID" code "$EXIT_CODE"
echo "============================================"

_thulu_post_flags
_thulu_post_comment "$OUTPUT_DIR/comment.md"
_thulu_post_index

exit $EXIT_CODE
