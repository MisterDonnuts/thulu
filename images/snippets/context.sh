#!/usr/bin/env bash

{
  printf '\n'
  msg agent.prompts.context instance "$INSTANCE_ID" output_dir "${PROMPT_OUTPUT_DIR:-$OUTPUT_DIR}" \
    timestamp "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '%s\n' "${EXTRA_CONTEXT:-}"
} >> "$MERGED"

printf '\n%s\n' "$(msg agent.prompts.output_only output_dir "${PROMPT_OUTPUT_DIR:-$OUTPUT_DIR}" \
  projects_dir "${OCTOPUS_ROOT:-/workspace}/projects/")" >> "$MERGED"
