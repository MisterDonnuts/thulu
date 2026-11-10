#!/usr/bin/env bash
# Installed as /usr/local/bin/claude when the image is built with CLAUDE_CODE_INSTALL=first-run, the
# default: published images carry no copy of Claude Code. On first use it installs the pinned,
# unmodified package from the npm registry on the machine that runs it, then execs it.
# The Dockerfile replaces the version placeholder below.
#
# The install lands in OCTOPUS_ENGINE_DIR: a volume every agent container of the install shares in
# Docker mode, the pod's own home in Kubernetes.
set -euo pipefail

_cc_version="@CLAUDE_CODE_VERSION@"
# An image built FROM a published one can install Claude Code here at build time, for machines with
# no route to the npm registry: RUN OCTOPUS_ENGINE_DIR=/opt/thulu-engines-image claude --version
_cc_baked="/opt/thulu-engines-image/claude-code-$_cc_version/bin/claude"
if [ -x "$_cc_baked" ]; then
  exec "$_cc_baked" "$@"
fi
_cc_dir="${OCTOPUS_ENGINE_DIR:-${HOME:-/home/node}/.cache/thulu-engines}"
_cc_home="$_cc_dir/claude-code-$_cc_version"
_cc_bin="$_cc_home/bin/claude"

if [ -x "$_cc_bin" ]; then
  exec "$_cc_bin" "$@"
fi

# The image copies the snippets to /workspace; a runner's OCTOPUS_ROOT never reaches this shim.
# shellcheck source=/dev/null
source "${OCTOPUS_ROOT:-/workspace}/snippets/i18n.sh"

mkdir -p "$_cc_dir"
_cc_lock="$_cc_dir/.claude-code-$_cc_version.lock"
[ -e "$_cc_lock" ] || : >"$_cc_lock" 2>/dev/null || true
# Read-only is enough for flock, so a lock file another user created still works.
exec 9<"$_cc_lock"
_cc_wait="${OCTOPUS_ENGINE_INSTALL_WAIT:-600}"
if ! flock -w "$_cc_wait" 9; then
  echo "ERROR: $(msg_en agent.errors.claude_install_busy seconds "$_cc_wait" version "$_cc_version" dir "$_cc_dir")" >&2
  exit 1
fi

if [ ! -x "$_cc_bin" ]; then
  msg_en agent.log.claude_installing version "$_cc_version" >&2
  _cc_start=$(date +%s)
  # A half-finished install never takes the final name: it is built aside and moved in whole.
  _cc_tmp=$(mktemp -d "$_cc_dir/.claude-code-$_cc_version.XXXXXX")
  if ! npm install --global --prefix "$_cc_tmp" --cache "$_cc_tmp/.npm-cache" \
    --no-audit --no-fund --no-update-notifier --loglevel=error --logs-max=0 \
    --fetch-retries="${NPM_CONFIG_FETCH_RETRIES:-5}" \
    --fetch-retry-maxtimeout="${NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT:-120000}" \
    "@anthropic-ai/claude-code@$_cc_version" >&2 || [ ! -x "$_cc_tmp/bin/claude" ]; then
    rm -rf "$_cc_tmp"
    echo "ERROR: $(msg_en agent.errors.claude_install_failed version "$_cc_version")" >&2
    exit 1
  fi
  rm -rf "$_cc_tmp/.npm-cache"
  chmod 755 "$_cc_tmp"
  rm -rf "$_cc_home"
  mv "$_cc_tmp" "$_cc_home"
  msg_en agent.log.claude_installed version "$_cc_version" seconds "$(($(date +%s) - _cc_start))" >&2
fi
exec 9<&-

exec "$_cc_bin" "$@"
