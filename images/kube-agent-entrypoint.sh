#!/usr/bin/env bash
: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir
export OCTOPUS_ROOT

set -euo pipefail

# shellcheck source=/dev/null
source "${OCTOPUS_ROOT}/snippets/i18n.sh"

if [ -z "${AGENT_NAME:-}" ]; then
  echo "ERROR: $(msg_en agent.errors.agent_name_required)" >&2
  exit 1
fi

source "${OCTOPUS_ROOT}/snippets/run-bundle.sh"

exec "${OCTOPUS_ROOT}/entrypoint.sh"
