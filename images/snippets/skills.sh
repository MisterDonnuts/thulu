#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -z "${MERGED:-}" ] || [ ! -f "$MERGED" ]; then
  echo "WARN: $(msg_en agent.log.skills_needs_merged)" >&2
  return 0 2>/dev/null || exit 0
fi

SKILLS_DIR="${SKILLS_DIR:-${OCTOPUS_ROOT}/skills}"

if [ ! -d "$SKILLS_DIR" ]; then
  echo "WARN: $(msg_en agent.log.skills_dir_missing dir "$SKILLS_DIR")" >&2
  return 0 2>/dev/null || exit 0
fi

_skills_append() {
  local name="$1"
  local dir="$SKILLS_DIR/$name"
  if [ ! -f "$dir/prompt.md" ]; then
    echo "WARN: $(msg_en agent.log.skill_no_prompt_in name "$name" dir "$dir")" >&2
    return
  fi
  printf '\n---\n\n%s\n\n' "$(msg agent.prompts.skill_heading name "$name")" >> "$MERGED"
  cat "$dir/prompt.md" >> "$MERGED"
  if [ -d "$dir/scripts" ] && [ -n "$(ls -A "$dir/scripts" 2>/dev/null)" ]; then
    printf '\n%s\n' "$(msg agent.prompts.skill_scripts name "$name")" >> "$MERGED"
    for script in "$dir/scripts"/*; do
      [ -f "$script" ] && printf -- '- `%s`\n' "$script" >> "$MERGED"
    done
  fi
  local extra
  extra=$(cd "$dir" && find . -type f ! -name '.*' ! -path './scripts/*' ! -path './prompt.md' ! -path './skill.yaml' 2>/dev/null |
    sed 's|^\./||' | LC_ALL=C sort)
  if [ -n "$extra" ]; then
    printf '\n%s\n' "$(msg agent.prompts.skill_files name "$name")" >> "$MERGED"
    printf '%s\n' "$extra" | while IFS= read -r rel; do
      printf -- '- `%s/%s`\n' "$dir" "$rel" >> "$MERGED"
    done
  fi
}

if [ -d "$SKILLS_DIR/_base" ]; then
  _skills_append "_base"
fi

if [ -n "${SKILLS:-}" ]; then
  IFS=',' read -ra _SKILL_LIST <<< "$SKILLS"
  for skill in "${_SKILL_LIST[@]}"; do
    skill=$(echo "$skill" | xargs)  # trim
    [ -z "$skill" ] && continue
    if [ ! -d "$SKILLS_DIR/$skill" ]; then
      echo "WARN: $(msg_en agent.log.skill_not_found name "$skill" dir "$SKILLS_DIR")" >&2
      continue
    fi
    _skills_append "$skill"
  done
fi

unset -f _skills_append
unset _SKILL_LIST
