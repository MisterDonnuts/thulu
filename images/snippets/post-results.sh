#!/usr/bin/env bash
# Each upload writes a .posted marker on 2xx so the daemon, which reads the same dir in Docker mode,
# does not post it again.

_thulu_post() {
  local -a auth_hdr=()
  [ -n "${OCTOPUS_API_KEY:-}" ] && auth_hdr=(-H "X-API-Key: ${OCTOPUS_API_KEY}")
  curl -sf -X POST "$OCTOPUS_API$1" \
    -H "Content-Type: $2" \
    ${auth_hdr[@]+"${auth_hdr[@]}"} \
    --data-binary @"$3" > /dev/null 2>&1 && touch "$4"
}

_thulu_post_comment() {
  local file="$1"
  local marker="${file%.md}.posted"
  [ -f "$file" ] || return 0
  [ -f "$marker" ] && return 0
  [ -z "${TASK_ID:-}" ] || [ -z "${OCTOPUS_API:-}" ] && return 0
  local tmp
  tmp=$(mktemp)
  # i18n-ignore JavaScript, not a message
  COMMENT_FILE="$file" node -e "
    const fs = require('fs');
    const body = fs.readFileSync(process.env.COMMENT_FILE, 'utf8');
    const parent_id = process.env.THREAD_ID || undefined;
    fs.writeFileSync('$tmp', JSON.stringify({ body, parent_id }));
  " 2>/dev/null || { rm -f "$tmp"; return 0; }
  _thulu_post "/tasks/$TASK_ID/comments" application/json "$tmp" "$marker" || true
  rm -f "$tmp"
}

_thulu_post_index() {
  local file="$OUTPUT_DIR/_index.md"
  local marker="$OUTPUT_DIR/_index.posted"
  [ -f "$file" ] || return 0
  [ -f "$marker" ] && return 0
  [ -z "${TASK_ID:-}" ] || [ -z "${OCTOPUS_API:-}" ] && return 0
  local tmp
  tmp=$(mktemp)
  # i18n-ignore JavaScript, not a message
  REPORT_FILE="$file" node -e "
    const fs = require('fs');
    const body = fs.readFileSync(process.env.REPORT_FILE, 'utf8');
    const instance_id = process.env.INSTANCE_ID || '';
    fs.writeFileSync('$tmp', JSON.stringify({ kind: 'agent_index', body, instance_id }));
  " 2>/dev/null || { rm -f "$tmp"; return 0; }
  if ! _thulu_post "/tasks/$TASK_ID/reports" application/json "$tmp" "$marker"; then
    echo "WARN: $(msg_en agent.log.index_upload_failed)" >&2
  fi
  rm -f "$tmp"
}

_thulu_post_flags() {
  local file="$OUTPUT_DIR/flags.yaml"
  local marker="$OUTPUT_DIR/flags.posted"
  [ -f "$file" ] || return 0
  [ -f "$marker" ] && return 0
  [ -z "${TASK_ID:-}" ] || [ -z "${OCTOPUS_API:-}" ] && return 0
  if ! _thulu_post "/tasks/$TASK_ID/flags/import" application/x-yaml "$file" "$marker"; then
    echo "WARN: $(msg_en agent.log.flags_upload_failed)" >&2
  fi
}

_thulu_post_auditor_feedback() {
  local file="$OUTPUT_DIR/feedback.md"
  local marker="$OUTPUT_DIR/feedback.posted"
  [ -f "$file" ] || return 0
  [ -f "$marker" ] && return 0
  [ -z "${TASK_ID:-}" ] || [ -z "${OCTOPUS_API:-}" ] && return 0
  [ -z "${INSTANCE_ID:-}" ] && return 0
  if ! _thulu_post "/tasks/$TASK_ID/auditor-feedback?instance=${INSTANCE_ID}" text/markdown "$file" "$marker"; then
    echo "WARN: $(msg_en agent.log.feedback_upload_failed)" >&2
  fi
}
