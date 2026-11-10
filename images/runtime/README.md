# octopus-runtime

Base image for Thulu **task sandboxes**: a bare container where the manager runs `docker exec` for each task's builds, tests and dev servers, without polluting the agent's container.

## What it includes

- Node 22 LTS on Debian 13 (base `node:22-trixie-slim`, pinned by digest)
- Python 3 + pip
- git, build-essential, curl, jq, less, iproute2, netcat
- procps (`ps`, `pkill`, `top`...)

If a task needs a more specific stack (Go, Rust, deno, etc.), the agent can install it through `exec` with `apt-get install` or the language's package manager. The sandbox lasts as long as the task, so keep the image light.

## Build

```bash
docker build -t octopus-runtime images/runtime/
```

Thulu builds it automatically the first time it needs a sandbox. Rebuild by hand only if the Dockerfile changes.

## How Thulu uses it

The manager (`internal/sandbox/`) handles the whole lifecycle. The flow:

1. The agent runs and reaches `mcp__thulu-sandbox__exec` → Thulu's MCP gateway receives the call
2. If the task has no sandbox and there is a free slot → it launches a container from this image, mounts the worktree at `/workspace/projects/<name>/`, and pre-maps common ports (3000, 5173, 8000, 8080) with dynamic offsets
3. Every later `exec` is a `docker exec` inside it
4. `preview_url(port)` reads the mapped host port from the inspect
5. When the task reaches Done or sits idle for more than 30 min, the manager destroys it

## No MCP server inside

This image is a pure runtime. The MCP gateway lives in Thulu (Go): a single coordination point with a queue and per-stage priorities.
