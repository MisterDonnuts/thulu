#!/usr/bin/env bash

engine_session_emit() {
  local model="${ENGINE_SESSION_MODEL:-}"
  model=${model//\"/}
  model=${model//\\/}
  if [ -n "$model" ]; then
    # i18n-ignore a stream-json event, not a message
    printf '{"type":"octopus.session","mode":"%s","model":"%s"}\n' "$1" "$model"
    return
  fi
  # i18n-ignore a stream-json event, not a message
  printf '{"type":"octopus.session","mode":"%s"}\n' "$1"
}

engine_session_compose() {
  local message="$1"
  if [ -n "${RESUME_BASE_PROMPT:-}" ] && [ -s "$RESUME_BASE_PROMPT" ]; then
    cat "$RESUME_BASE_PROMPT"
    printf '\n\n---\n\n'
  fi
  if [ -n "${CONVERSATION_FILE:-}" ] && [ -s "$CONVERSATION_FILE" ]; then
    printf '%s\n\n' "$(msg agent.prompts.session_transcript)"
    cat "$CONVERSATION_FILE"
    printf '\n\n---\n\n'
  fi
  printf '%s\n\n' "$(msg agent.prompts.session_message)"
  cat "$message"
}

# No jq: the agent image does not ship it. Lines with octopus_injected are steer-driver.js echoes,
# not engine output.
engine_session_guard() {
  local failed="$1" marker="$2" line got=0
  local -a held=()
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
    *'"octopus_injected":true'*)
      held+=("$line")
      line=""
      continue
      ;;
    esac
    got=1
    break
  done
  if [ "$got" = 0 ] || "$failed" "$line"; then
    : >"$marker"
    cat >/dev/null
    return 0
  fi
  engine_session_emit native
  [ "${#held[@]}" -gt 0 ] && printf '%s\n' "${held[@]}"
  printf '%s\n' "$line"
  cat
}

# Subshell so errexit and pipefail, which engine-run.sh sets, cannot cut the fallback short.
engine_session_run() (
  set +e +o pipefail
  failed_fn="$1" restore_fn="$2" resume_fn="$3" fresh_fn="$4"
  work=$(mktemp -d "${TMPDIR:-/tmp}/octopus-session.XXXXXX") || exit 1
  trap 'rm -rf "$work"' EXIT
  message="$work/message"
  cat >"$message"

  # A resume whose snapshot was lost (ResumeStage) arrives with RESUME_MESSAGE and
  # RESUME_BASE_PROMPT but no SESSION_ID; it is rebuilt below from the transcript.
  if [ -z "${SESSION_ID:-}" ] && { [ -z "${RESUME_MESSAGE:-}" ] || [ -z "${RESUME_BASE_PROMPT:-}" ]; }; then
    engine_session_emit fresh
    "$fresh_fn" <"$message"
    exit $?
  fi

  if [ -n "${SESSION_ID:-}" ] && "$restore_fn"; then
    "$resume_fn" <"$message" | engine_session_guard "$failed_fn" "$work/resume-failed"
    rc=${PIPESTATUS[0]}
    [ -f "$work/resume-failed" ] || exit "$rc"
    echo "WARN: $(msg_en agent.log.session_resume_failed session "$SESSION_ID")" >&2
  fi

  mode=fresh
  [ -n "${CONVERSATION_FILE:-}" ] && [ -s "$CONVERSATION_FILE" ] && mode=rehydrated
  engine_session_compose "$message" >"$work/prompt"
  engine_session_emit "$mode"
  ENGINE_ECHO_FILE="$message" "$fresh_fn" <"$work/prompt"
)
