#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -z "${STAGE:-}" ] || [ -z "${MERGED:-}" ]; then
  return 0
fi

PROJECTS_ROOT="${PROJECTS_ROOT:-${OCTOPUS_ROOT}/projects}"
if [ ! -d "$PROJECTS_ROOT" ]; then
  return 0
fi

HOOK_LOG_DIR="$OUTPUT_DIR/.project-hooks"
mkdir -p "$HOOK_LOG_DIR" 2>/dev/null || true

# Indexed arrays, not associative: the snippet must also run on bash 3.2 (macOS) for tests.
_HK_NAME=()
_HK_PID=()
_HK_LOG=()
_hook_index() {
  local i
  for i in "${!_HK_NAME[@]}"; do
    if [ "${_HK_NAME[$i]}" = "$1" ]; then
      echo "$i"
      return 0
    fi
  done
}
for proj_dir in "$PROJECTS_ROOT"/*/; do
  [ -d "$proj_dir" ] || continue
  name="$(basename "$proj_dir")"
  hook_file="$proj_dir.thulu/hooks/$STAGE.sh"
  [ -f "$hook_file" ] || continue
  log_file="$HOOK_LOG_DIR/${name}-${STAGE}.log"
  echo "[project-stage] $(msg_en agent.log.hook_running project "$name" stage "$STAGE")"
  (
    cd "$proj_dir" 2>/dev/null || exit 99
    bash "$hook_file"
  ) >"$log_file" 2>&1 &
  _HK_NAME+=("$name")
  _HK_PID+=("$!")
  _HK_LOG+=("$log_file")
done

ANY_INJECTED=0
_inject_header() {
  [ "$ANY_INJECTED" = "1" ] && return 0
  printf '\n%s\n' "$(msg agent.prompts.project_context_header)" >>"$MERGED"
  ANY_INJECTED=1
}

for proj_dir in "$PROJECTS_ROOT"/*/; do
  [ -d "$proj_dir" ] || continue
  name="$(basename "$proj_dir")"
  thulu_dir="$proj_dir.thulu"
  prompt_file="$thulu_dir/prompts/$STAGE.md"

  if [ -f "$prompt_file" ]; then
    _inject_header
    printf '\n%s\n\n' "$(msg agent.prompts.project_stage_prompt project "$name" stage "$STAGE")" >>"$MERGED"
    cat "$prompt_file" >>"$MERGED"
    echo "[project-stage] $(msg_en agent.log.project_prompt_injected project "$name" stage "$STAGE")"
  fi

  idx="$(_hook_index "$name")"
  [ -n "$idx" ] || continue
  pid="${_HK_PID[$idx]}"
  set +e
  wait "$pid"
  HOOK_EXIT=$?
  set -e
  log_file="${_HK_LOG[$idx]}"

  tail_output="$(tail -n 80 "$log_file" 2>/dev/null)"
  line_count="$(wc -l <"$log_file" 2>/dev/null | tr -d ' ')"

  _inject_header
  printf '\n%s\n\n' "$(msg agent.prompts.project_hook_result project "$name" stage "$STAGE" \
    exit_code "$HOOK_EXIT" dir "$PROJECTS_ROOT/$name/" lines "${line_count:-?}" output "$tail_output")" >>"$MERGED"
done
