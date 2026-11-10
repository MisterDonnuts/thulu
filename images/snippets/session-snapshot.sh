#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

SESSION_STATE_DIR="${SESSION_STATE_DIR:-${OCTOPUS_ROOT}/.engine-session}"
CONVERSATION_FILE="${CONVERSATION_FILE:-${OCTOPUS_ROOT}/.conversation.md}"

_octopus_session_get() {
  local -a auth_hdr=()
  [ -n "${OCTOPUS_API_KEY:-}" ] && auth_hdr=(-H "X-API-Key: ${OCTOPUS_API_KEY}")
  # --retry-connrefused: a new pod's NetworkPolicy is programmed asynchronously (see run-bundle.sh).
  curl -sSL --connect-timeout 3 --max-time 60 --retry 1 --retry-connrefused -w '%{http_code}' \
    ${auth_hdr[@]+"${auth_hdr[@]}"} -o "$2" "$OCTOPUS_API$1" 2>/dev/null || true
}

_octopus_session_fetch() {
  [ -n "${SESSION_ID:-}" ] || [ -n "${RESUME_MESSAGE:-}" ] || return 0
  [ -n "${TASK_ID:-}" ] && [ -n "${OCTOPUS_API:-}" ] || return 0
  local tmp code
  tmp=$(mktemp)

  rm -rf "$SESSION_STATE_DIR"
  if [ -n "${SESSION_ID:-}" ]; then
    mkdir -p "$SESSION_STATE_DIR"
    code=$(_octopus_session_get "/tasks/$TASK_ID/session-snapshot?sid=$SESSION_ID" "$tmp")
    if [ "$code" = "200" ] && tar -xzf "$tmp" -C "$SESSION_STATE_DIR" --no-same-owner 2>/dev/null; then
      msg_en agent.log.session_snapshot_restored session "$SESSION_ID" dir "$SESSION_STATE_DIR"
    else
      msg_en agent.log.session_snapshot_missing session "$SESSION_ID" code "$code"
    fi
  fi

  rm -f "$CONVERSATION_FILE"
  code=$(_octopus_session_get "/tasks/$TASK_ID/conversation-transcript?agent=${AGENT_NAME:-}" "$tmp")
  if [ "$code" = "200" ] && [ -s "$tmp" ]; then
    cp "$tmp" "$CONVERSATION_FILE"
    msg_en agent.log.session_transcript_saved file "$CONVERSATION_FILE"
  else
    msg_en agent.log.session_transcript_missing code "$code"
  fi
  rm -f "$tmp"
}

_octopus_session_upload() {
  local dir="$OUTPUT_DIR/.engine-session" sid="" tgz code
  rm -rf "$dir"
  mkdir -p "$dir"
  if declare -f engine_session_save >/dev/null 2>&1; then
    engine_session_save "$dir" || true
  fi
  [ -s "$dir/id" ] && sid=$(head -1 "$dir/id")
  printf '%s\n' "$sid" >"$OUTPUT_DIR/.session_id"
  if [ -z "$sid" ]; then
    msg_en agent.log.session_none
    rm -rf "$dir"
    return 0
  fi
  [ -n "${TASK_ID:-}" ] && [ -n "${OCTOPUS_API:-}" ] || { rm -rf "$dir"; return 0; }

  tgz=$(mktemp)
  if tar -czf "$tgz" -C "$dir" . 2>/dev/null; then
    local -a auth_hdr=()
    [ -n "${OCTOPUS_API_KEY:-}" ] && auth_hdr=(-H "X-API-Key: ${OCTOPUS_API_KEY}")
    code=$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 3 --max-time 120 \
      --retry 2 --retry-delay 1 --retry-connrefused \
      -X PUT -H "Content-Type: application/gzip" ${auth_hdr[@]+"${auth_hdr[@]}"} \
      --data-binary @"$tgz" "$OCTOPUS_API/tasks/$TASK_ID/session-snapshot?sid=$sid" 2>/dev/null || true)
  else
    code="tar"
  fi
  case "$code" in
  2??) msg_en agent.log.session_snapshot_uploaded session "$sid" ;;
  *) echo "WARN: $(msg_en agent.log.session_upload_failed session "$sid" code "$code")" >&2 ;;
  esac
  rm -f "$tgz"
  rm -rf "$dir/state"
}
