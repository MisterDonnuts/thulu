#!/usr/bin/env bash

: "${OCTOPUS_ROOT:=/workspace}" # a runner on a host points it at its run dir

if [ -z "${MERGED:-}" ] || [ ! -f "$MERGED" ]; then
  echo "WARN: $(msg_en agent.log.skills_native_needs_merged)" >&2
  return 0 2>/dev/null || exit 0
fi

SKILLS_DIR="${SKILLS_DIR:-${OCTOPUS_ROOT}/skills}"
NATIVE_DIR=""
declare -f engine_skills_dir >/dev/null 2>&1 && NATIVE_DIR=$(engine_skills_dir)

if [ -z "$NATIVE_DIR" ]; then
  echo "WARN: $(msg_en agent.log.skills_native_no_dir engine "${ENGINE:-?}")" >&2
  return 0 2>/dev/null || exit 0
fi

if [ ! -d "$SKILLS_DIR" ]; then
  echo "WARN: $(msg_en agent.log.skills_native_dir_missing dir "$SKILLS_DIR")" >&2
  return 0 2>/dev/null || exit 0
fi

mkdir -p "$NATIVE_DIR"

# .skill-meta (key=value) is written by the daemon when it builds the bundle; skill.yaml is the
# fallback for bundles from older daemons.
_skill_field() {
  local dir="$1" key="$2"
  if [ -f "$dir/.skill-meta" ]; then
    sed -n "s/^$key=//p" "$dir/.skill-meta" | head -1
    return
  fi
  sed -n "s/^$key:[[:space:]]*//p" "$dir/skill.yaml" 2>/dev/null | head -1 | sed 's/[[:space:]]*$//'
}

_skill_extra_files() {
  local dir="$1"
  (cd "$dir" && find . -type f ! -name '.*' ! -path './scripts/*' ! -path './prompt.md' ! -path './skill.yaml' 2>/dev/null) |
    sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r rel; do
      printf '%s/%s\n' "$dir" "$rel"
    done
}

_native_count=0
_inline_names=""

_materialize() {
  local name="$1"
  local dir="$SKILLS_DIR/$name"
  local yaml="$dir/skill.yaml"
  local body="$dir/prompt.md"
  [ -f "$body" ] || { echo "WARN: $(msg_en agent.log.skill_no_prompt name "$name")" >&2; return; }

  local enforcement="on_demand" desc=""
  if [ -f "$yaml" ]; then
    desc=$(_skill_field "$dir" description)
    local declared
    declared=$(_skill_field "$dir" enforcement)
    [ -n "$declared" ] && enforcement="$declared"
  fi

  # enforcement: always must stay in the prompt; the model may never open a native skill.
  if [ "$enforcement" = "always" ]; then
    _inline_names="${_inline_names:+$_inline_names,}$name"
    return
  fi

  [ -z "$desc" ] && desc="$(msg agent.prompts.skill_fallback_description name "$name")"

  mkdir -p "$NATIVE_DIR/$name"
  {
    printf -- '---\n'
    printf 'name: %s\n' "$name" # i18n-ignore SKILL.md frontmatter
    # Quote: descriptions contain ':' and the engine silently skips invalid YAML. Escape backslashes
    # before quotes.
    # i18n-ignore SKILL.md frontmatter
    printf 'description: "%s"\n' "$(printf '%s' "$desc" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
    printf -- '---\n\n'
    cat "$body"
    if [ -d "$dir/scripts" ] && [ -n "$(ls -A "$dir/scripts" 2>/dev/null)" ]; then
      printf '\n%s\n\n' "$(msg agent.prompts.native_skill_scripts)"
      for script in "$dir/scripts"/*; do
        [ -f "$script" ] && printf -- '- `%s`\n' "$script"
      done
    fi
    local extra
    extra=$(_skill_extra_files "$dir")
    if [ -n "$extra" ]; then
      printf '\n%s\n\n' "$(msg agent.prompts.native_skill_files)"
      printf '%s\n' "$extra" | while IFS= read -r f; do printf -- '- `%s`\n' "$f"; done
    fi
  } > "$NATIVE_DIR/$name/SKILL.md"
  _native_count=$((_native_count + 1))
}

if [ -n "${SKILLS:-}" ]; then
  IFS=',' read -ra _NATIVE_LIST <<< "$SKILLS"
  for skill in "${_NATIVE_LIST[@]}"; do
    skill=$(echo "$skill" | xargs)
    [ -z "$skill" ] && continue
    [ -d "$SKILLS_DIR/$skill" ] || { echo "WARN: $(msg_en agent.log.skill_not_in_bundle name "$skill")" >&2; continue; }
    _materialize "$skill"
  done
fi

# skills.sh, sourced next by the caller, pastes whatever is left in SKILLS.
SKILLS="$_inline_names"
export SKILLS

msg_en agent.log.skills_native_summary count "$_native_count" dir "$NATIVE_DIR" \
  inline "${_inline_names:-$(msg_en agent.log.only_base)}"

unset -f _skill_field _skill_extra_files _materialize
unset _NATIVE_LIST _native_count _inline_names
