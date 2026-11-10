#!/usr/bin/env bash
# Forced, as in git-native.sh: a rebased branch is rewritten. A clone's origin may embed a key:
# strip it and push through the env helper.
# Sourced by entrypoint-base.sh: never `exit` (it still has to save the session and run the auditor)
# or change shell options outside a subshell.
: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -n "${OCTOPUS_RESULT_REF:-}" ]; then
  # Literal on purpose: git expands ${OCTOPUS_API_KEY} when it runs the helper.
  # shellcheck disable=SC2016
  # i18n-ignore a git credential helper, not a message
  _octopus_git_helper='!f() { test "$1" = get || exit 0; echo username=octopus; echo "password=${OCTOPUS_API_KEY}"; }; f'
  for repo in "${OCTOPUS_ROOT}"/projects/*/; do
    [ -d "${repo}.git" ] || continue
    (
      cd "$repo" || exit 0
      git add -A
      if ! git diff --cached --quiet; then
        git -c user.name="Thulu" -c user.email="noreply@thulu.dev" \
          commit -m "$(msg_en agent.labels.result_commit instance "${INSTANCE_ID:-run}")" >/dev/null 2>&1 || true
      fi
      origin_url=$(git remote get-url origin 2>/dev/null || true)
      clean_url=$(printf '%s' "$origin_url" | sed -E 's#^([A-Za-z][A-Za-z0-9+.-]*://)[^/@]*@#\1#')
      if [ -n "$origin_url" ] && [ "$clean_url" != "$origin_url" ]; then
        git remote set-url origin "$clean_url" || true
      fi
      helper_args=()
      if [ -n "${OCTOPUS_API_KEY:-}" ]; then
        helper_args=(-c credential.helper= -c "credential.helper=${_octopus_git_helper}")
      fi
      if ! git "${helper_args[@]}" push origin "+HEAD:${OCTOPUS_RESULT_REF}" >/dev/null 2>&1; then
        echo "[result-push] $(msg_en agent.log.result_push_failed ref "$OCTOPUS_RESULT_REF" repo "$repo")" >&2
      fi
    )
  done
  unset _octopus_git_helper
fi
