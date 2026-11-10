# thulu-sandbox

MCP service that exposes a Linux runtime container so agents can run builds, tests, dev servers and any heavy command they **do not want to run inside the agent's container**.

**The implementation lives in the Go daemon** (`internal/sandbox/mcp_server.go`), not here: the daemon serves the MCP at `<AGENT_BASE_URL>/sandbox/sse` and the dispatcher injects that URL per task. There is no standalone container to build or start (see `docs/mcp-server-deploy.md`).

This directory keeps only the metadata other components read:

| File | Reader |
|---------|-----------|
| `tool.yaml` | `internal/api/handle_tools.go`: tool catalog for the API/UI |
| `skill.md` | `images/snippets/mcp-skills.sh`: appended to the agent's prompt when `MCP_TOOLS` includes `thulu-sandbox` |

## Exposed tools

| Tool | Description |
|------|-------------|
| `exec` | Runs a shell command in the sandbox. Returns `stdout`, `stderr`, `exit_code`, `duration_ms`, `timed_out`. Supports `cwd`, `timeout_ms` and extra `env` |
| `preview_url` | Returns the URL of a port the sandbox publishes (e.g. a dev server) |
| `attach_file` | Attaches a sandbox file to the task, read on the server side |
| `init_status` | Per project of the task: whether it has an init script, its manifest and the state of the last run in this sandbox |
| `init_run` | Runs the project's init script (stopping the previous run) and waits for the `ready` command |
| `init_script_get` | Reads the project's current init script |
| `init_script_update` | Saves a new revision of the script, signed by the agent's run |

Init scripts are stored in Postgres per project, with history (`internal/project/initscript.go`); the dashboard edits them on the project page, and the runner that executes them in the sandbox is in `internal/sandbox/init.go`.

The contract details (defaults, timeouts, output truncation, traps) are in `skill.md`, which is what the agent sees.

## How an agent uses it

The tools are available as `mcp__thulu-sandbox__<tool>` (for example `mcp__thulu-sandbox__init_run`) when `MCP_TOOLS` includes `thulu-sandbox`. The dispatcher injects the SSE URL per task; it is not configured by hand in the agent's compose.
