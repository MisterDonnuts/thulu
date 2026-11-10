# thulu-browser

MCP server that exposes Chromium (through Playwright) as a tool agents can use.

## Exposed tools

29 tools, declared in `server.js` (the `browser_*` ones and the two without a session) and `evidence/tools.js` (the `evidence_*` ones). The catalog for the picker is in `tool.yaml`.

| Group | Tools | What they do |
|------|-------|-----------|
| No session | `screenshot`, `get_text` | PNG or `innerText` of a URL, each in a fresh Chromium context |
| Session | `browser_new_session`, `browser_close_session` | Opens an isolated `BrowserContext` and returns `session_id` (with `record_video=true` it records a `.webm`); closing it frees resources |
| Navigation and reading | `browser_navigate`, `browser_get_state`, `browser_get_text`, `browser_screenshot` | Go to a URL, list interactive elements with an index, visible text and a capture of the current page |
| Interaction | `browser_click`, `browser_type`, `browser_select`, `browser_scroll`, `browser_hover`, `browser_press_key` | By `get_state` index or CSS selector. `browser_type` with `secret` keeps the value out of the log, the response and the take; in a recording chat, `secret` without `text` asks Thulu for the value |
| Time | `browser_wait` | A pause of up to 10 000 ms |
| Scripts | `browser_run_script`, `browser_list_scripts` | Runs a Playwright script (by name, path or inline code) inside this Node process |
| Log | `browser_get_session_log`, `browser_export_script` | The session's JSON log and a reusable Playwright script generated from it |
| Evidence | `evidence_start`, `evidence_shot`, `evidence_verify`, `evidence_note`, `evidence_finish`, `evidence_get_take`, `evidence_render`, `evidence_publish` | Filmed takes with cursor and captions, annotated captures, verdicts per test case, before/after and publishing to the task |
| Evidence library | `evidence_recording`, `evidence_save_case` | In a recording chat: which case to record and in which environment, and saving the take as the case's recording (it queues a first run) |

The agent's usage manual is in `skill.md`.

### How captures and videos reach the task

With a task to upload them to, images do not pass through the model's context as base64. If the upload fails, the tool says so in the response and returns the image inline.

- `screenshot` and `browser_screenshot` with `task_id` (or a session opened with `task_id`): the server uploads the full-resolution PNG as a task attachment, through the daemon's API (`OCTOPUS_API`) and with the key of the run that opened the session. The response carries the attachment id and not the image.
- `evidence_shot` outside a take also uploads the annotated capture to the task.
- Video: `evidence_finish` builds a draft (MP4 and contact sheet) and `evidence_publish(render_id)` uploads the video, captures, chapters and take to the task, publishes the report in the Reports tab and records the test case runs. The `record_video` `.webm` does not reach the task.
- Without `task_id`, `screenshot` and `browser_screenshot` return the image inline for the agent to look at. It costs vision tokens and is not stored.

`instance_id` + `agent_type` are still accepted and write to `<AGENTS_ROOT>/<agent_type>/runs/<instance_id>/` (captures, and the `.webm` when the session closes). The `deploy/` stacks and the chart mount a scratch directory there that the daemon does not read, so that reaches no task.

### Evidence library runner

The same image runs the library's batches (`docs/evidence.md`, "Running a batch") with `node evidence/job.js` and three variables: `OCTOPUS_API`, `OCTOPUS_API_KEY` (the batch's key) and `EVIDENCE_BATCH_ID`. It asks for the next run (`POST /evidence/jobs/{batch}/next`), replays the take with `renderTake` against the environment's base URL and with its test logins, uploads video, take, chapters, poster and captures (`.../runs/{run}/artifacts`), sends the result (`.../result`) and asks again until it gets 204. It never allows steps with side effects: a take recorded with `changes_data` comes back as `error`. It retries 5xx answers and dropped connections; login values and the key appear in no log, report or error. Each run works in `/tmp` and deletes its files once they are uploaded.

## Build

```bash
docker build -t thulu-browser tools/thulu-browser/
```

## Run (manual, dev)

The usual way is `docker compose -f tools/docker-compose.yml up -d thulu-browser`. By hand, with the same hardening the `deploy/` stacks use:

```bash
docker run -d -p 127.0.0.1:3010:3000 \
  --network octopus-agents --network-alias thulu-browser \
  --user 1000:1000 --read-only --tmpfs /tmp --tmpfs /agents-shared \
  --cap-drop ALL --security-opt no-new-privileges --shm-size 1g \
  -e HOME=/tmp -e OCTOPUS_API=http://octopus:8585 \
  --name thulu-browser thulu-browser
```

Endpoints:
- `GET  /sse`: MCP transport (SSE). It asks for the run's key (`X-API-Key` or
  `Authorization: Bearer`); without a key, 401. `?catalog=1` without a key
  opens a session that only lists tools.
- `POST /messages?sessionId=...`: MCP messages. Each one has to carry the same
  key as its session's handshake; otherwise, 401.
- `GET  /health`: liveness check

Variables:

| Variable | Default | Effect |
|---|---|---|
| `PORT` | `3000` | port |
| `LISTEN_HOST` | `0.0.0.0` | interface; the per-run mode uses `127.0.0.1` |
| `MCP_REQUIRED_KEY` | empty | if set, the only key that opens a session (catalog included). The daemon sets it on the per-run browser to that run's key |
| `MCP_ALLOWED_TOOLS` | empty | comma-separated list of tools: the most a session can see and call; `?tools=` only shrinks it. The daemon sets it on the per-run browser when the stage grants only some tools |
| `MCP_CATALOG_ONLY` | empty | `1`: it only opens the catalog session (`?catalog=1`) and runs no tool. For the shared server when each run has its own |
| `OCTOPUS_API` | none | the daemon's URL, to upload captures and evidence |

## Shared or per run

With `OCTOPUS_BROWSER_MODE=per-run` on the daemon (the default with
`OCTOPUS_SAAS_MODE=1`) each run that has thulu-browser on the stage gets its
own: a sidecar in the agent's pod (Kubernetes) or a container in the agent's
network namespace (Docker), on `127.0.0.1:3010` and with
`MCP_REQUIRED_KEY` = the run's key. The reason: `browser_run_script` runs the
agent's JavaScript inside this Node process, and in a shared server that
script can read the other sessions' keys. Details in
[`docs/mcp-integration.md`](../../docs/mcp-integration.md#per-run-thulu-browser).

## Trying it by hand

```bash
# Liveness
curl -s http://localhost:3000/health

# Listing tools and calling screenshot over MCP needs an MCP client.
# For a quick check, connect from an agent with its MCP config pointing at
# http://host.docker.internal:3000/sse  (macOS/Windows)  or
# http://172.17.0.1:3000/sse              (Linux with the default bridge)
```

## How an agent uses it

The dispatcher puts `thulu-browser` in `MCP_TOOLS` when the stage grants it to the agent type, and `images/snippets/mcp-config.sh` writes it into `/workspace/.mcp.json` with the URL of the daemon's proxy (`/mcp-proxy/thulu-browser`; in per-run mode, the loopback). The proxy adds the `X-API-Key` header with the run's key if the MCP registration has "Acts on Thulu for the agent" ticked; without a key the server answers 401. Claude Code discovers the server at startup and the tools are named `mcp__thulu-browser__<tool>`. Details of the wiring and of the `@` suffixes in [`docs/mcp-integration.md`](../../docs/mcp-integration.md).

## Notes

- **Context per session**: `screenshot` and `get_text` open a new `browserContext` per call. For login + action, use `browser_new_session` and the `session_id` it returns; an unused session closes after 10 minutes (`SESSION_IDLE_MS`).
- **Browser singleton**: the process boots Chromium once, on the first call. In shared mode, several agents in parallel share that browser and that Node process, each with its own context; the context isolates pages, not the JavaScript of `browser_run_script`. Hence the per-run mode.
- **Auth per session, not per network**: each session needs a run key and each message repeats it. Even so, do not expose it publicly: a run's key is enough to open a session on a shared server.
- **express.json() is forbidden**: `SSEServerTransport` reads the request stream directly. With a global body parser it fails with `stream is not readable`. Do not add `app.use(express.json())`.
- **Only headless Chromium in the image**: the server launches Chromium with no window (`lib/browser.js`), so the Dockerfile installs only Playwright's headless shell, with the `playwright` that `package-lock.json` pins: library and browser cannot end up on different versions. Launching Firefox, WebKit or a windowed Chromium (`headless: false`, `channel`) means changing the Dockerfile's `playwright install`.
