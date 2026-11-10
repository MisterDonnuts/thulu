#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

set +eo pipefail

_STAGE="${STAGE:-default}"

AUD_PROMPT_DIR="${AUDITOR_PROMPT_DIR:-${OCTOPUS_ROOT}/auditor}"
AUD_PROMPT_FILE=""
if [ -f "$AUD_PROMPT_DIR/${_STAGE}.md" ]; then
  AUD_PROMPT_FILE="$AUD_PROMPT_DIR/${_STAGE}.md"
elif [ -f "$AUD_PROMPT_DIR/default.md" ]; then
  AUD_PROMPT_FILE="$AUD_PROMPT_DIR/default.md"
fi

echo ""
echo "============================================"
msg_en agent.log.auditor_banner stage "$_STAGE"
echo "============================================"

AUD_MERGED="${OCTOPUS_TMP:-/tmp}/auditor-prompt.md"
if [ -n "$AUD_PROMPT_FILE" ]; then
  cat "$AUD_PROMPT_FILE" > "$AUD_MERGED"
else
  msg agent.prompts.auditor_generic > "$AUD_MERGED"
fi

# The auditor sees the run only through the API brief, so nothing the agent left on disk can pose as
# the verdict.
# --retry-connrefused: a pod's connections are refused until the NetworkPolicy controller programs
# its IP.
_aud_brief() {
  local -a auth_hdr=()
  [ -n "${OCTOPUS_API_KEY:-}" ] && auth_hdr=(-H "X-API-Key: ${OCTOPUS_API_KEY}")
  if [ -z "${TASK_ID:-}" ] || [ -z "${OCTOPUS_API:-}" ] ||
     ! curl -fsSL --connect-timeout 3 --max-time 15 --retry 1 --retry-connrefused ${auth_hdr[@]+"${auth_hdr[@]}"} \
       "$OCTOPUS_API/tasks/$TASK_ID/brief?audit=${AUDITED_INSTANCE:-}" 2>/dev/null; then
    msg agent.prompts.auditor_no_brief
  fi
}

SKILLS_DIR="${SKILLS_DIR:-${OCTOPUS_ROOT}/skills}"
_aud_append_skill() {
  local name="$1"
  local dir="$SKILLS_DIR/$name"
  [ -f "$dir/prompt.md" ] || { echo "WARN: $(msg_en agent.log.auditor_skill_no_prompt name "$name")" >&2; return; }
  printf '\n---\n\n%s\n\n' "$(msg agent.prompts.skill_heading name "$name")" >> "$AUD_MERGED"
  cat "$dir/prompt.md" >> "$AUD_MERGED"
}
[ -d "$SKILLS_DIR/_base" ] && _aud_append_skill "_base"
[ -d "$SKILLS_DIR/quality-review" ] && _aud_append_skill "quality-review"

if [ -n "${AUDITOR_SKILLS:-}" ]; then
  IFS=',' read -ra _AUD_SKILL_LIST <<< "$AUDITOR_SKILLS"
  for skill in "${_AUD_SKILL_LIST[@]}"; do
    skill=$(echo "$skill" | xargs)
    [ -z "$skill" ] && continue
    [ "$skill" = "_base" ] && continue
    [ "$skill" = "quality-review" ] && continue
    _aud_append_skill "$skill"
  done
fi

_aud_exit_note=""
[ "${EXIT_CODE:-0}" = "1" ] && _aud_exit_note="$(msg agent.prompts.auditor_max_turns)"
printf '\n---\n\n%s\n' "$(msg agent.prompts.auditor_context agent "${AUDITED_AGENT:-$AGENT_NAME}" \
  stage "$_STAGE" instance "${AUDITED_INSTANCE:-?}" exit_code "${EXIT_CODE:-0}" exit_note "$_aud_exit_note" \
  timestamp "$(date -u +%Y-%m-%dT%H:%M:%SZ)" brief "$(_aud_brief)" output_dir "${PROMPT_OUTPUT_DIR:-$OUTPUT_DIR}" \
  projects_dir "${OCTOPUS_ROOT}/projects/")" >> "$AUD_MERGED"

chmod 644 "$AUD_MERGED" 2>/dev/null || true

# The daemon reads .thulu-auditor-prompt.md for the auditor_prompt timeline event; keep the name.
cp "$AUD_MERGED" "$OUTPUT_DIR/.thulu-auditor-prompt.md" 2>/dev/null || true

AUD_ENGINE="${AUDITOR_ENGINE:-${ENGINE:-claude-code}}"
AUD_MODEL="${AUDITOR_MODEL:-${MODEL:-}}"
AUD_TURNS="${AUDITOR_TURNS:-5}"

msg_en agent.log.auditor_settings engine "$AUD_ENGINE" model "${AUD_MODEL:-$(msg_en agent.log.default)}" turns "$AUD_TURNS" \
  skills "_base, quality-review${AUDITOR_SKILLS:+, $AUDITOR_SKILLS}"
echo ""

# When not root (Kubernetes runAsNonRoot), run in place: gosu fails with "operation not permitted".
_AUD_HOME="${HOME:-/home/node}"
_AUD_RUN=(env ENGINE="$AUD_ENGINE" MODEL="$AUD_MODEL" MAX_TURNS="$AUD_TURNS" EFFORT=
  MCP_CONFIG_ARGS= SESSION_ID= STEER_ENABLED=0 SESSION_STATE_DIR= CONVERSATION_FILE=
  RESUME_BASE_PROMPT= OCTOPUS_TOKEN_OPTIMIZER= "${OCTOPUS_ROOT}/snippets/engine-run.sh")
if [ "$(id -u)" = "0" ]; then
  _AUD_HOME=/home/node
  _AUD_RUN=(env HOME=/home/node gosu node "${_AUD_RUN[@]}")
fi
"${_AUD_RUN[@]}" < "$AUD_MERGED" 2>&1 | tee "$OUTPUT_DIR/auditor-session.log"
AUD_EXIT=${PIPESTATUS[0]}
AUDITOR_EXIT=$AUD_EXIT

_aud_session_id() {
  local dir
  dir=$(mktemp -d)
  (
    export HOME="$_AUD_HOME"
    # shellcheck source=/dev/null
    source "${OCTOPUS_ROOT}/snippets/engines/${AUD_ENGINE}.sh" 2>/dev/null &&
      declare -f engine_session_save >/dev/null &&
      engine_session_save "$dir"
  ) >/dev/null 2>&1
  [ -s "$dir/id" ] && head -1 "$dir/id"
  rm -rf "$dir"
}
_aud_session_id > "$OUTPUT_DIR/.auditor_session_id" 2>/dev/null || true

echo ""
msg_en agent.log.auditor_finished code "$AUD_EXIT"
if [ -f "$OUTPUT_DIR/feedback.md" ]; then
  msg_en agent.log.auditor_feedback path "$OUTPUT_DIR/feedback.md"
else
  echo "  WARN: $(msg_en agent.log.auditor_no_feedback)"
fi
echo "============================================"
echo ""

unset -f _aud_append_skill _aud_brief _aud_session_id 2>/dev/null || true
unset _AUD_SKILL_LIST _AUD_RUN _AUD_HOME _aud_exit_note AUD_MERGED AUD_PROMPT_FILE AUD_ENGINE AUD_MODEL AUD_TURNS AUD_EXIT
