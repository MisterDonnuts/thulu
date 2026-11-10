#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

set -eo pipefail

# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/i18n.sh"

PROMPT_DIR="${OCTOPUS_ROOT}/instructions/prompts"
MCP_ARGS_FILE="${OCTOPUS_ROOT}/.mcp-args"
MODEL="${CHAT_MODEL:-${MODEL:-}}"
MAX_TURNS="${CHAT_MAX_TURNS:-${MAX_TURNS:-30}}"

mkdir -p "${OCTOPUS_ROOT}/runs"

MESSAGE="$(cat)"
if [ -z "$MESSAGE" ]; then
    jq -cn --arg e "$(msg_en agent.errors.empty_message)" '{type: "error", error: $e}' >&2
    exit 2
fi

ENGINE="${ENGINE:-claude-code}"

MCP_ARGS=""
[ -f "$MCP_ARGS_FILE" ] && MCP_ARGS="$(cat "$MCP_ARGS_FILE")"

# Not /tmp: its sticky bit blocks files created by a different uid.
INPUT_FILE="$(mktemp "${OCTOPUS_ROOT}/runs/chat-input.XXXXXX.md")"
trap 'rm -f "$INPUT_FILE"' EXIT

SYSTEM_PROMPT="$PROMPT_DIR/chat.md"
[ -f "$SYSTEM_PROMPT" ] || SYSTEM_PROMPT="$PROMPT_DIR/main.md"
[ -f "$SYSTEM_PROMPT" ] || SYSTEM_PROMPT=""

if [ -n "${SESSION_ID:-}" ]; then
    printf "%s" "$MESSAGE" > "$INPUT_FILE"
else
    if [ -n "$SYSTEM_PROMPT" ]; then
        cat "$SYSTEM_PROMPT" > "$INPUT_FILE"
        printf '\n\n---\n\n%s\n\n%s\n' "$(msg agent.prompts.chat_first_message)" "$MESSAGE" >> "$INPUT_FILE"
    else
        printf "%s" "$MESSAGE" > "$INPUT_FILE"
    fi
fi
chown node:node "$INPUT_FILE" 2>/dev/null || true

# max_turns exits 1; that is still a valid turn whose output we want.
set +eo pipefail

OUTPUT_LOG="${OCTOPUS_ROOT}/runs/${CHAT_ID:-chat}.jsonl"

# Adapters expect MCP_CONFIG_ARGS. Kubernetes runs as uid 1000 without setuid, where gosu fails
# "operation not permitted".
# i18n-ignore a shell script, not a message
TURN_SCRIPT="
    export HOME=/home/node
    export ENGINE='$ENGINE' MODEL='$MODEL' MAX_TURNS='$MAX_TURNS' EFFORT='${EFFORT:-}' MCP_CONFIG_ARGS='$MCP_ARGS'${SESSION_ID:+ SESSION_ID='$SESSION_ID'}
    export RESUME_BASE_PROMPT='$SYSTEM_PROMPT'
    cat '$INPUT_FILE' | '${OCTOPUS_ROOT}/snippets/engine-run.sh'
"
if [ "$(id -u)" = "0" ]; then
    gosu node bash -c "$TURN_SCRIPT" | tee "$OUTPUT_LOG"
else
    bash -c "$TURN_SCRIPT" | tee "$OUTPUT_LOG"
fi

exit ${PIPESTATUS[0]}
