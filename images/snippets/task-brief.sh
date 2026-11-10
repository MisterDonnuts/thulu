#!/usr/bin/env bash

if [ -z "${TASK_ID:-}" ]; then
  return 0
fi

if [ -z "${MERGED:-}" ]; then
  echo "[task-brief] WARN: $(msg_en agent.log.brief_no_merged)"
  return 0
fi

OCTOPUS_API="${OCTOPUS_API:-http://host.docker.internal:8585}"

echo "[task-brief] $(msg_en agent.log.brief_fetching task "$TASK_ID" api "$OCTOPUS_API")"

AUTH_HDR=()
if [ -n "${OCTOPUS_API_KEY:-}" ]; then
  AUTH_HDR=(-H "X-API-Key: ${OCTOPUS_API_KEY}")
fi

BRIEF_URL="$OCTOPUS_API/tasks/$TASK_ID/brief"
[ -n "${THREAD_ID:-}" ] && BRIEF_URL="$BRIEF_URL?thread=$THREAD_ID"

BRIEF_TMP="${OCTOPUS_TMP:-/tmp}/task-brief-$TASK_ID.md"
# `|| true` outside the substitution: an inner `|| echo 000` appended to the status -f already
# printed ("404000").
# --retry-connrefused: see run-bundle.sh.
HTTP_CODE=$(curl -fsSL --connect-timeout 3 --max-time 8 --retry 1 --retry-connrefused \
  -w '%{http_code}' ${AUTH_HDR[@]+"${AUTH_HDR[@]}"} -o "$BRIEF_TMP" "$BRIEF_URL" 2>/dev/null) || true
[ -n "$HTTP_CODE" ] || HTTP_CODE="000"

if [ "$HTTP_CODE" != "200" ] || [ ! -s "$BRIEF_TMP" ]; then
  echo "[task-brief] WARN: $(msg_en agent.log.brief_fetch_failed code "$HTTP_CODE")"
  return 0
fi

printf '\n%s\n\n' "$(msg agent.prompts.brief_header)" >> "$MERGED"
cat "$BRIEF_TMP" >> "$MERGED"

if [ "${STAGE:-}" = "mention" ]; then
  if [ -n "${MENTION_COMMENT_BODY:-}" ]; then
    printf '\n%s\n' "$(msg agent.prompts.brief_mention_comment comment "$MENTION_COMMENT_BODY" \
      thread "${THREAD_ID:-}")" >> "$MERGED"
  fi
  printf '\n%s\n\n' "$(msg agent.prompts.brief_mention_finish output_dir "${PROMPT_OUTPUT_DIR:-$OUTPUT_DIR}" \
    thread "${THREAD_ID:-}" projects_dir "${OCTOPUS_ROOT:-/workspace}/projects/")" >> "$MERGED"
else
  printf '\n%s\n\n' "$(msg agent.prompts.brief_stage_finish output_dir "${PROMPT_OUTPUT_DIR:-$OUTPUT_DIR}")" >> "$MERGED"
fi

rm -f "$BRIEF_TMP"
echo "[task-brief] $(msg_en agent.log.brief_injected)"
