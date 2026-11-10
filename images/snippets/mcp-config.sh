#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

MCP_CONFIG_FILE="${OCTOPUS_ROOT}/.mcp.json"
_MCP_SNIPPETS="$(dirname "${BASH_SOURCE[0]}")"
[ -f "$_MCP_SNIPPETS/mcp-servers.js" ] || _MCP_SNIPPETS="${OCTOPUS_ROOT}/snippets"

_mcp_listed=$(node "$_MCP_SNIPPETS/mcp-servers.js" names)
IFS=',' read -ra _MCP_ENTRIES <<< "${MCP_TOOLS:-}"
for _mcp_entry in "${_MCP_ENTRIES[@]}"; do
  _mcp_name=$(echo "${_mcp_entry%%@*}" | xargs) # trim
  [ -z "$_mcp_name" ] && continue
  if ! grep -qxF -- "$_mcp_name" <<< "$_mcp_listed"; then
    echo "WARNING: $(msg_en agent.log.mcp_server_not_listed name "$_mcp_name")"
  fi
done

if [ -z "$_mcp_listed" ]; then
  echo "WARNING: $(msg_en agent.log.mcp_no_servers file "$MCP_CONFIG_FILE")"
  unset _mcp_listed _MCP_ENTRIES _mcp_entry _mcp_name _MCP_SNIPPETS
  return 0 2>/dev/null || exit 0
fi

node "$_MCP_SNIPPETS/mcp-json.js" "$MCP_CONFIG_FILE"

# Docker starts the per-run thulu-browser after the agent (it joins this netns); wait for /health or
# the CLI's first connect marks the server failed.
if [ "${OCTOPUS_BROWSER_PER_RUN:-}" = "1" ] && [ -n "${MCP_THULU_BROWSER_URL:-}" ]; then
  _browser_health="${MCP_THULU_BROWSER_URL%%\?*}"
  _browser_health="${_browser_health%/sse}/health"
  _browser_wait=60
  if ! node "$_MCP_SNIPPETS/mcp-wait.js" "$_browser_health" "$_browser_wait"; then
    echo "WARNING: $(msg_en agent.log.mcp_browser_not_ready url "$_browser_health" seconds "$_browser_wait")"
  fi
  unset _browser_health _browser_wait
fi
unset _mcp_listed _MCP_ENTRIES _mcp_entry _mcp_name _MCP_SNIPPETS

export MCP_CONFIG_ARGS="--mcp-config $MCP_CONFIG_FILE"

msg_en agent.log.mcp_config_written file "$MCP_CONFIG_FILE"
# Mask the run key: stdout goes verbatim to /data/logs, which is readable over the API.
sed 's/"oct_[A-Za-z0-9_-]*"/"oct_***"/g' "$MCP_CONFIG_FILE"
