#!/usr/bin/env bash
# Fails hard on purpose: a run without its prompts and rules is a different agent. task-brief.sh may
# degrade; this must not.

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

set -uo pipefail

BUNDLE_API="${OCTOPUS_API:-}"
BUNDLE_KEY="${OCTOPUS_API_KEY:-}"

if [ -z "$BUNDLE_API" ] || [ -z "$BUNDLE_KEY" ]; then
  echo "FATAL: $(msg_en agent.errors.bundle_no_api)" >&2
  exit 90
fi

BUNDLE_TMP="$(mktemp -d)"
BUNDLE_TGZ="$BUNDLE_TMP/bundle.tar.gz"

# --retry-connrefused: kube-router programs a new pod's NetworkPolicy asynchronously and the daemon
# port REJECTs until then (~0.4 s on k3s, longer with a new namespace).
# No -f: the 4xx body explains the failure. `|| true` because kube-agent-entrypoint.sh sources this
# under set -e.
HTTP_CODE=$(curl -sSL --connect-timeout 3 --max-time 30 \
  --retry 4 --retry-delay 2 --retry-connrefused \
  -w '%{http_code}' -H "X-API-Key: ${BUNDLE_KEY}" \
  -o "$BUNDLE_TGZ" "${BUNDLE_API}/run/bundle?stage=${STAGE:-}" 2>/dev/null) || true
[ -n "$HTTP_CODE" ] || HTTP_CODE="000"

if [ "$HTTP_CODE" != "200" ] || [ ! -s "$BUNDLE_TGZ" ]; then
  echo "FATAL: $(msg_en agent.errors.bundle_fetch_failed url "${BUNDLE_API}/run/bundle" code "$HTTP_CODE")" >&2
  if [ -s "$BUNDLE_TGZ" ] && head -c 1 "$BUNDLE_TGZ" | grep -q '{'; then
    echo "       $(msg_en agent.errors.bundle_daemon_said body "$(head -c 500 "$BUNDLE_TGZ")")" >&2
  fi
  echo "       $(msg_en agent.errors.bundle_refusing)" >&2
  rm -rf "$BUNDLE_TMP"
  exit 91
fi

if ! tar -xzf "$BUNDLE_TGZ" -C "$BUNDLE_TMP"; then
  echo "FATAL: $(msg_en agent.errors.bundle_unreadable)" >&2
  rm -rf "$BUNDLE_TMP"
  exit 92
fi

# Mirrors prompt-init.sh: prompts/$STAGE.md, else prompts/main.md.
if [ ! -f "$BUNDLE_TMP/prompts/${STAGE:-}.md" ] && [ ! -f "$BUNDLE_TMP/prompts/main.md" ]; then
  echo "FATAL: $(msg_en agent.errors.bundle_no_identity stage "${STAGE:-?}")" >&2
  echo "       $(msg_en agent.errors.bundle_contents files "$(find "$BUNDLE_TMP" -maxdepth 2 | tr '\n' ' ')")" >&2
  rm -rf "$BUNDLE_TMP"
  exit 93
fi

mkdir -p "${OCTOPUS_ROOT}/instructions"
rm -rf "${OCTOPUS_ROOT}/instructions/prompts" "${OCTOPUS_ROOT}/auditor" "${OCTOPUS_ROOT}/skills" "${OCTOPUS_ROOT}/tools"
mv "$BUNDLE_TMP/prompts" "${OCTOPUS_ROOT}/instructions/prompts"
[ -d "$BUNDLE_TMP/auditor" ] && mv "$BUNDLE_TMP/auditor" "${OCTOPUS_ROOT}/auditor"
[ -d "$BUNDLE_TMP/skills" ] && mv "$BUNDLE_TMP/skills" "${OCTOPUS_ROOT}/skills"
[ -d "$BUNDLE_TMP/tools" ] && mv "$BUNDLE_TMP/tools" "${OCTOPUS_ROOT}/tools"
rm -rf "$BUNDLE_TMP"

msg_en agent.log.bundle_unpacked agent "${AGENT_NAME:-?}" stage "${STAGE:-?}" \
  skills "$(ls "${OCTOPUS_ROOT}/skills" 2>/dev/null | tr '\n' ' ')"
