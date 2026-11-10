# OpenCode and OpenAI-compatible providers

> Claude Code is the main engine. OpenCode runs agents and auditors too; live messages to a running agent and the runner on your machine need Claude Code.

OpenCode (`opencode`, the `opencode-ai` npm package) is the third engine, next
to Claude Code and codex. What sets it apart is that its LLM profile picks the
provider: the Anthropic API, the OpenAI API, or any server that speaks OpenAI's
Chat Completions API (DeepSeek, OpenRouter, vLLM, Ollama). With that an agent
can run on a model that comes from neither Anthropic nor OpenAI.

That does not make Thulu engine-independent: each engine has its own adapter
and parser, and what one supports another may not (see the table at the end).
The engine contract is in [engines.md](engines.md).

## Providers

| `provider` | The API OpenCode speaks | `base_url` | Key |
|---|---|---|---|
| `openai-compatible` (default) | Chat Completions: `POST <base_url>/chat/completions` | required: there is no default address | `Authorization: Bearer` |
| `anthropic` | Messages: `POST <base_url>/v1/messages` | optional; without it, the Anthropic API | `x-api-key` |
| `openai` | Responses: `POST <base_url>/responses` | optional; without it, the OpenAI API | `Authorization: Bearer` |

The `base_url` goes in as the provider publishes it: for `anthropic` without
`/v1` (like Claude Code's `ANTHROPIC_BASE_URL`; the adapter adds it), for the
other two with the path the server already serves.

The key is not validated by prefix: OpenCode's credential type has no shape of
its own, because it depends on the provider the profile picks. On save,
Thulu tests it against the provider's model listing (`GET <base_url>/models`
for `openai-compatible`).

## Example: DeepSeek

Through Chat Completions:

```bash
curl -X POST "$OCTOPUS/llm-profiles" -H "X-API-Key: $KEY" -H 'Content-Type: application/json' -d '{
  "name": "deepseek",
  "engine": "opencode",
  "provider": "openai-compatible",
  "base_url": "https://api.deepseek.com",
  "model": "deepseek-chat",
  "key": "sk-…",
  "is_default": true
}'
```

DeepSeek also publishes an Anthropic-shaped API. The same profile with
`"provider": "anthropic"` and `"base_url": "https://api.deepseek.com/anthropic"`
uses it. That endpoint also works for Claude Code (a `claude-code` profile
with that `base_url`).

In the UI: Settings → AI engines → OpenCode, pick the provider, fill in the
base URL, the model and the key.

Other servers:

| Server | `provider` | `base_url` | `model` |
|---|---|---|---|
| OpenRouter | `openai-compatible` | `https://openrouter.ai/api/v1` | the OpenRouter id, e.g. `qwen/qwen3-coder` |
| vLLM | `openai-compatible` | `http://vllm.llm.svc:8000/v1` | the model it serves |
| Ollama | `openai-compatible` | `http://ollama.llm.svc:11434/v1` | e.g. `qwen2.5-coder:32b` |

An `http://` `base_url`, or one at an internal address, is accepted only if its
host is in `OCTOPUS_LLM_ALLOW_HTTP_HOSTS` (see [admin-guide.md](admin-guide.md)).

Without `model`, the adapter runs `deepseek-chat`, which exists only on
DeepSeek: a profile for another server has to name its model, and so does one
for `anthropic` or `openai` (the adapter stops the run if it is missing).

## Where the key goes

With the LLM gateway on, the agent's container never holds the profile's key.
The dispatcher sets:

| Variable | Value |
|---|---|
| `OCTOPUS_LLM_PROVIDER` | the profile's provider |
| `OCTOPUS_LLM_BASE_URL` | `<gateway>/<provider>`, e.g. `…/llm-gateway/openai-compatible` |
| `LLM_API_KEY` | the run's key |

The gateway receives `/<provider>/<path>`, checks that the provider is the one
in the run's profile, swaps the run's key for the profile's and forwards to the
profile's `base_url`. With the gateway off, `OCTOPUS_LLM_BASE_URL` is the
profile's `base_url` and `LLM_API_KEY` is the key itself.

## Isolated config

OpenCode gathers config from many layers: each org's config from `auth.json`
(downloaded from `.well-known/opencode`), the global one in
`~/.config/opencode`, the project's (`opencode.json`, `.opencode/`,
`AGENTS.md` walking up from the cwd), `~/.opencode`, `OPENCODE_CONFIG`,
`OPENCODE_CONFIG_DIR`. The merge is deep: any layer can add an MCP server, a
plugin or instructions.

`images/snippets/engines/opencode.sh` writes the whole run config into
`OPENCODE_CONFIG_CONTENT` (the top layer) and closes the others:

- `OPENCODE_AUTH_CONTENT={}`: no stored keys and no org config;
- `OPENCODE_DISABLE_PROJECT_CONFIG=1`: nothing from the project's repo;
- `OPENCODE_PURE=1`, `OPENCODE_DISABLE_CLAUDE_CODE=1`,
  `OPENCODE_DISABLE_EXTERNAL_SKILLS=1`: no external plugins, no `~/.claude`
  and no `~/.agents`;
- `OPENCODE_CONFIG` and `OPENCODE_CONFIG_DIR` unset;
- `~/.config/opencode` and `~/.opencode` emptied before each run, and the
  first one left read-only. Anything it finds there that is not OpenCode's is
  named on stderr.

The generated config (`images/snippets/opencode-config.js`) carries no secrets:
it names `{env:LLM_API_KEY}` and `{env:OCTOPUS_API_KEY}` and OpenCode reads
them from the environment. `enabled_providers` keeps only the profile's, and
`share` is `disabled`.

`internal/engine/conformance_test.go` (`TestConformance_IsolatedConfig`)
requires these switches in the adapter.

## MCP

Each server in `OCTOPUS_MCP_SERVERS` goes in as `"type": "remote"` with
`oauth: false`; the ones that ask for `run_key` carry
`X-API-Key: {env:OCTOPUS_API_KEY}`. `opencode-config.js` reads the list with
`mcp-servers.js`, the same reader the Claude Code and Codex writers use, so the
three engines see the same servers (nearly all through `/mcp-proxy/<name>`,
see [mcp-proxy.md](mcp-proxy.md)). OpenCode tries streamable HTTP and falls
back to SSE on its own, so `transport` changes nothing. Tools are named
`<server>_<tool>`, e.g. `octopus-api_task_get`.

## What it supports

| | OpenCode |
|---|---|
| Resume a session | yes: `opencode export` at the end, `opencode import` and `run --session` on resume |
| MCP | yes |
| Skills on disk | yes (`skills.paths`) |
| `MAX_TURNS` | no: an OpenCode agent's `steps` ends the run with a normal answer, which the stream does not tell apart; a run never ends in `max_turns` |
| `EFFORT` | no: `--variant` takes different values per model |
| Steering | no |
| Cost | what OpenCode reports for models in its catalog; for the rest, the estimate from the `deepseek-chat` and `deepseek-reasoner` prices |

OpenCode prints one JSON line per event (`step_start`, `text`, `tool_use`,
`step_finish` with tokens and cost, `error`), with no start or result line.
The parser (`internal/engine/opencode/parse.go`) builds both: the `EventInit`
from the first `sessionID` and the `EventResult` at the end, with the text of
the last step that wrote something and the summed tokens.

## Version

`images/docker/Dockerfile` pins `OPENCODE_VERSION=1.18.35` and installs only
the binary package for the platform (`opencode-linux-x64` or
`opencode-linux-arm64`, about 185 MB). The fixtures in
`internal/engine/testdata/opencode` are captures of that version against a
fake Chat Completions server; bumping the version means capturing them again.
