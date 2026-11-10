#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -z "${MCP_TOOLS:-}" ]; then
  return 0 2>/dev/null || exit 0
fi

if [ -z "${MERGED:-}" ] || [ ! -f "$MERGED" ]; then
  echo "WARN: $(msg_en agent.log.mcp_skills_needs_merged)" >&2
  return 0 2>/dev/null || exit 0
fi

TOOLS_DIR="${TOOLS_DIR:-${OCTOPUS_ROOT}/tools}"
if [ ! -d "$TOOLS_DIR" ]; then
  echo "WARN: $(msg_en agent.log.mcp_tools_not_mounted dir "$TOOLS_DIR")" >&2
  return 0 2>/dev/null || exit 0
fi

_appended=0
IFS=',' read -ra _MCP_NAMES <<< "$MCP_TOOLS"
for entry in "${_MCP_NAMES[@]}"; do
  entry=$(echo "$entry" | xargs)
  [ -z "$entry" ] && continue
  # MCP_TOOLS entries may carry an @scope suffix (thulu-github@write), as in mcp-config.sh; the
  # skill dir is the bare name.
  name="${entry%%@*}"
  skill_file="$TOOLS_DIR/$name/skill.md"
  if [ ! -f "$skill_file" ]; then
    continue
  fi
  printf '\n---\n\n%s\n\n' "$(msg agent.prompts.tool_heading name "$name")" >> "$MERGED"
  cat "$skill_file" >> "$MERGED"
  _appended=$((_appended + 1))
done

if [ "$_appended" -gt 0 ]; then
  msg_en agent.log.mcp_skills_injected names "$_appended"
fi

unset _MCP_NAMES _appended
