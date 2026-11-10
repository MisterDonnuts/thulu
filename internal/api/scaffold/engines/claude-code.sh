#!/usr/bin/env bash
set -euo pipefail

if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "[agent:claude-code] ERROR: neither CLAUDE_CODE_OAUTH_TOKEN nor ANTHROPIC_API_KEY is set" >&2
  echo "[agent:claude-code]        set a Claude key under Settings → AI Engines," >&2
  echo "[agent:claude-code]        or paste a token into <repo>/.env." >&2
  exit 1
fi

if ! command -v claude >/dev/null 2>&1; then
  echo "[agent:claude-code] ERROR: 'claude' CLI not on PATH inside the container" >&2
  exit 1
fi

claude --version
echo "[agent:claude-code] auth ok"

claude -p "Reply with exactly: Thulu agent OK" --no-continue 2>/dev/null || true
