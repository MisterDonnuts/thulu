#!/usr/bin/env bash
# Push is forced: a rebase rewrites the branch, and the daemon refuses to drop a commit the agent's
# copy lacks.
# Sourced by entrypoint-base.sh: never `exit` or change shell options outside a subshell.
: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -n "${OCTOPUS_RESULT_REF:-}" ]; then
  for repo in "${OCTOPUS_ROOT}"/projects/*/; do
    [ -d "${repo}.git" ] || continue
    (
      cd "$repo" || exit 0
      git config remote.origin.push "+HEAD:refs/heads/${OCTOPUS_RESULT_REF}"
      git config --replace-all remote.origin.fetch "+refs/remotes/origin/*:refs/remotes/origin/*"
      # i18n-ignore a git identity, not a message
      git config user.name >/dev/null 2>&1 || git config user.name "Thulu"
      # i18n-ignore a git identity, not a message
      git config user.email >/dev/null 2>&1 || git config user.email "noreply@thulu.dev"
    ) || true
  done
  export OCTOPUS_GIT_NATIVE=1
fi

# On a host the worktree is a real checkout with the machine's credentials; the runner pushes the
# task branch when the run ends.
[ "${OCTOPUS_HOST_RUN:-}" = "1" ] && export OCTOPUS_GIT_NATIVE=1
