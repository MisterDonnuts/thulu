# Sandbox

## What is a sandbox?

A **sandbox** is a short-lived Docker container that an agent uses to run code safely. Agents don't execute code locally — they call `exec` and `preview_url` MCP tools over SSE. The sandbox manager spins up a container, the agent drives it via JSON-RPC, and the container is released when the agent is done.

This gives agents an isolated, ephemeral environment for build/test/run operations without requiring them to have Docker access themselves.

---

## Architecture

```
Agent container
  │  calls MCP over SSE
  ▼
GET /sandbox/sse?task_id=abc123&stage=in_progress
POST /sandbox/messages   (JSON-RPC 2.0)
  │
  ▼
sandbox.MCPHandler
  │  resolves workspace paths for task (via WorkspaceResolver)
  │  acquires sandbox from Manager (lazy, queued)
  ▼
sandbox.Manager
  │  docker run <sandbox-image>
  │  mounts /workspace/projects/<name>/ from task worktrees
  │  sets memory limit (OCTOPUS_SANDBOX_MEMORY_BYTES, default 6 GB)
  ▼
Sandbox container
  (runs exec commands, serves preview URLs)
```

---

## MCP protocol

The sandbox gateway implements [MCP](https://modelcontextprotocol.io) over HTTP+SSE. Protocol version: `2024-11-05`.

### Establishing a session

```
GET /sandbox/sse?task_id=<id>&stage=<stage>
  → text/event-stream
  ← event: message
     data: {"jsonrpc":"2.0","id":null,"method":"initialize","params":{...}}
```

The SSE connection stays open for the duration of the session. Each session gets a unique `session_id`.

### Sending messages

```
POST /sandbox/messages?session_id=<session_id>
Content-Type: application/json

{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{...}}

→ 202 Accepted
```

Responses arrive as SSE events on the open `/sandbox/sse` connection. The 202 ack is immediate — processing is async. This is why the session's context lives on the SSE goroutine, not the POST handler.

---

## MCP tools

### `exec`

Run a shell command inside the sandbox container. Returns stdout + stderr + exit code.

```json
{
  "method": "tools/call",
  "params": {
    "name": "exec",
    "arguments": {
      "command": "npm test",
      "working_dir": "/workspace/projects/my-app",
      "timeout_seconds": 120
    }
  }
}
```

Response:
```json
{
  "result": {
    "stdout": "...",
    "stderr": "...",
    "exit_code": 0
  }
}
```

### `preview_url`

Get a publicly reachable URL for a port exposed by the sandbox. Useful for integration tests that need to hit an HTTP server running inside the container.

```json
{
  "method": "tools/call",
  "params": {
    "name": "preview_url",
    "arguments": {
      "port": 3000
    }
  }
}
```

Response:
```json
{
  "result": {
    "url": "https://thulu.example.com/preview/abc123/3000"
  }
}
```

The URL is constructed from `OCTOPUS_PUBLIC_HOST` (env). If not set, returns a localhost URL.

---

## Sandbox lifecycle

```
1. Agent connects to /sandbox/sse with task_id
2. MCPHandler calls Manager.Acquire(taskID, stage, workspacePaths)
3. Manager checks queue: if at capacity, request waits (priority from stage config)
4. Manager.docker.ContainerCreate → ContainerStart
5. Manager mounts worktrees at /workspace/projects/<name>/
6. Tool calls routed via MCPHandler → Manager.Exec / Manager.PreviewURL
7. Agent disconnects (SSE closes) → Manager.Release(sandboxID)
8. Manager.docker.ContainerStop → ContainerRemove
```

### Queue and capacity

`GET /settings/sandbox.max` controls the maximum number of concurrent sandboxes. Default: unlimited. When the queue is full, acquisition requests block and are served FIFO (with stage-based priority if configured).

```
GET /sandboxes          — list active sandboxes
GET /sandboxes/queue    — list pending acquisition requests
DELETE /tasks/{id}/sandbox  — force-release (admin)
```

### Memory limits

Set `OCTOPUS_SANDBOX_MEMORY_BYTES` (default `6442450944` = 6 GB). This is passed to Docker as the container memory limit. It must be ≤ the Docker VM's available RAM (on Colima: configured in `~/.colima/default/colima.yaml`).

---

## Workspace resolution

When a sandbox is acquired for a task, the `WorkspaceResolver` is called to discover the task's worktrees:

```go
// sandbox_resolver.go
func (s *Server) resolveTaskWorkspace(taskID string) (string, map[string]string, error) {
    task, _ := s.taskStore.Get(taskID)
    // returns task.WorktreePaths (project_id → host path)
}
```

The manager mounts each worktree read-write (or read-only for `mention` stage runs).

A task worktree is a linked git worktree whose `.git` file points at the project clone by the daemon's path (`/data/projects/<name>/.git/worktrees/<name>--task-<id>` with the daemon in a container). The sandbox, like agent and chat containers, also mounts that clone's `.git` read-only at the same path and gets `GIT_CONFIG_*` env marking `/workspace/projects/<name>` as a `safe.directory`, so `git log`, `status` and `diff` work in it. Commits go through the daemon. See `internal/types/worktreegit.go` and the Docker runtime note in [security-hardening.md](security-hardening.md#4-multi-tenancy--workspace-isolation).

---

## Authentication

The `/sandbox/sse` and `/sandbox/messages` endpoints require **API key auth**. They are intended for agent containers, not browsers.

Agents authenticate with the run's key, `OCTOPUS_API_KEY`, injected at launch.

---

## Metrics

- `octopus_sandboxes_active` (gauge) — incremented on Acquire, decremented on Release
- No per-sandbox duration metric; use Docker's own stats if needed
