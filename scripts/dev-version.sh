#!/usr/bin/env bash
# Must sort after the base release and before the next (semver) for the update check, janitor and
# audit log.
# The "g" prefix keeps an all-digit hash with a leading zero a valid semver identifier.
set -euo pipefail

cd "$(dirname "$0")/.."

tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)
base=${tag#v}
base=${base%%-*}
if [[ ! $base =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  base=0.0.0
  [[ $base =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]
fi
major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} patch=${BASH_REMATCH[3]}
# A prerelease tag (v0.2.0-rc.1) already names the next version; do not bump the patch.
if [[ $tag == *-* ]]; then
  next="$major.$minor.$patch"
else
  next="$major.$minor.$((patch + 1))"
fi

sha=$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
dirty=""
if ! git diff --quiet HEAD -- 2>/dev/null; then
  dirty=".dirty"
fi
echo "${next}-dev.g${sha}${dirty}"
