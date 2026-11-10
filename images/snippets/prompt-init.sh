#!/usr/bin/env bash

if [ -z "${PROMPT_SRC:-}" ]; then
  echo "ERROR: $(msg_en agent.errors.prompt_src_unset)"
  exit 1
fi

if [ -n "${STAGE:-}" ]; then
  STAGE_PROMPT="$(dirname "$PROMPT_SRC")/$STAGE.md"
  if [ -f "$STAGE_PROMPT" ]; then
    echo "PROMPT: $(msg_en agent.log.prompt_stage_using path "$STAGE_PROMPT")"
    PROMPT_SRC="$STAGE_PROMPT"
  fi
fi

if [ ! -f "$PROMPT_SRC" ]; then
  echo "ERROR: $(msg_en agent.errors.prompt_not_found path "$PROMPT_SRC")"
  exit 1
fi

MERGED="${OCTOPUS_TMP:-/tmp}/merged-prompt.md"
cat "$PROMPT_SRC" > "$MERGED"
PROMPT_FILE="$MERGED"
