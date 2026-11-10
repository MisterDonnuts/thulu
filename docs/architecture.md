# Architecture

## What Thulu is

Thulu is a daemon (`Go`) + web dashboard (`Astro`) that:

1. **Watches Docker** — monitors containers labeled `thulu.agent=true` and manages their lifecycle as "agents"
2. **Runs kanban boards** — user-customizable workflows (one board with 3 stages by default: Backlog, In progress, Done); each task carries the board it runs, flowing from that board's initial stage to a terminal one
3. **Manages projects** — persistent git repos with per-task worktrees mounted inside agent containers at `/workspace/projects/<name>/`
4. **Provides a sandbox MCP server** — agents call `exec` and `preview_url` tools over SSE to run code in isolated containers
5. **Handles authorization** — sign-in is delegated to an OIDC identity provider; Thulu keeps users, workspaces, roles, sessions and API keys scoped to a workspace (the dispatcher mints one per run)

The native MCP servers are named `thulu-*` (plus `octopus-api`), and the container and agent-definition labels use the `thulu.*` prefix.

## Component diagram

```
┌──────────────────────────────────────────────────────────────┐
│  Browser (Astro SPA — embedded in binary via go:embed)       │
│  src/pages/*.astro + src/lib/{api,auth,reactive}.ts          │
└────────────────────────┬─────────────────────────────────────┘
                         │ HTTP :8585 (JSON REST + SSE)
┌────────────────────────▼─────────────────────────────────────┐
│  api/server.go — HTTP mux + middleware chain                  │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────────┐  │
│  │ handle_  │ │ handle_  │ │ handle_  │ │ handle_sandbox │  │
│  │ tasks    │ │ projects │ │ agents   │ │ + /sandbox/sse  │  │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └───────┬────────┘  │
│       │            │            │               │            │
│  ┌────▼─────┐ ┌────▼─────┐ ┌────▼──────┐ ┌────▼──────────┐  │
│  │task.Store│ │project.  │ │registry. │ │sandbox.       │  │
│  │+Dispatcher│ │Store     │ │Registry  │ │Manager+MCPHandler│ │
│  └────┬─────┘ └────┬─────┘ └────┬──────┘ └───────────────┘  │
│       │            │            │                            │
│  ┌────▼─────────────▼────────────▼────────────────────────┐  │
│  │  Postgres (shared pool — pgx)  every store opens here   │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
│  watcher.Watcher ──────────────────────────────────────────► │
│  (Docker events API → injects agent into registry on start)  │
│                                                              │
│  auth.UserStore  audit.Store  usage.Store  secrets.Store     │
│  (one pool, one schema, per-store migrations namespaced)     │
└──────────────────────────────────────────────────────────────┘
                         │ Docker socket
┌────────────────────────▼─────────────────────────────────────┐
│  Docker / Colima                                             │
│  ┌───────────────┐  ┌───────────────┐  ┌──────────────────┐ │
│  │ agent container│  │ sandbox cont. │  │ MCP tool conts.  │ │
│  │ (labeled thulu │  │ (exec/preview │  │ thulu-browser    │ │
│  │  .agent=true)  │  │  MCP server)  │  │ thulu-github     │ │
│  └───────────────┘  └───────────────┘  └──────────────────┘ │
└──────────────────────────────────────────────────────────────┘
```

## Package layout

```
thulu/
├── main.go                  CLI: serve / status / logs / stop
├── internal/
│   ├── api/                 HTTP server, all route handlers
│   ├── agentstore/          Agent and skill catalog per workspace, system skills, revisions
│   ├── auth/                Users, workspaces, roles, JWT sessions, OIDC, API keys
│   ├── automation/          Cron/event/metric triggers, recipe engine, scheduler
│   ├── audit/               Immutable event log (Postgres)
│   ├── chat/                Chat transcripts (one per chat, persisted)
│   ├── daemon/              Startup wiring: opens all stores, starts server+watcher
│   ├── health/              /health /health/live /health/ready handlers
│   ├── metrics/             Prometheus registry + middleware + 8 metric families
│   ├── middleware/          Rate limiter, CORS, security headers, logger, body limit
│   ├── project/             Project store + worktree management (git ops)
│   ├── registry/            In-memory agent registry (watcher → API)
│   ├── sandbox/             Sandbox Manager, Docker lifecycle, MCP SSE server
│   ├── secrets/             LLM keys and profiles, per-agent bindings (encrypted at rest)
│   ├── settings/            Workspace settings (key→JSON store)
│   ├── stage/               Boards, stages, task types, approval requirements
│   ├── task/                Task store, Dispatcher, stage machine, deps
│   ├── types/               Stage constants, AgentType, compose file parser
│   ├── usage/               Token/cost collector + store
│   ├── watcher/             Docker event watcher
│   └── webhook/             GitHub webhook store + dispatcher
└── web/
    ├── embed.go             //go:embed all:dist
    └── src/
        ├── pages/           Astro pages
        └── lib/             api.ts, auth.ts, reactive.ts
```

## Middleware chain (inner → outer)

```
mux
  → RequestBodyLimit        (rejects oversized bodies)
  → SecurityHeaders         (CSP, X-Frame-Options, HSTS header hints)
  → CORS                    (configured by OCTOPUS_ALLOWED_ORIGINS)
  → JWTMiddleware           (injects Claims into context if bearer token present)
  → MiddlewareWithStore     (API key auth — env OCTOPUS_API_KEY or DB-issued keys)
  → RateLimiter             (per-IP, in-memory sliding window)
  → metrics.Middleware      (records http_requests_total + duration)
  → RequestID               (injects X-Request-ID)
  → Logger                  (structured JSON request log line)
```

## Data flow: task dispatch

```
User creates task (POST /tasks)
        │
        ▼
task.Store.Create()  →  stage = the board's initial stage (no worktree yet)
        │
User moves task to In progress (PATCH /tasks/:id  {stage:"in_progress"})
        │  hard dependencies gate leaving the initial stage
        ▼
task.Dispatcher.OnStageChange()
        │  resolves agent type for this stage from task.StageAssignments
        │  (none → no run; a system comment when the board staffs the column elsewhere)
        │  creates worktree per linked project (project.Store.CreateTaskWorktree)
        │  opens the PR if the stage has open_pr
        ▼
taskLauncher.Launch()  →  docker run (via types.Compose)
        │  mounts /workspace/projects/<name>/ per project worktree
        │  injects TASK_ID, STAGE, MODEL, MAX_TURNS, MCP_TOOLS, SKILLS...
        ▼
Agent container runs, exits
        │
OnExit callback → watcher detects container exit
        │  daemon commits each worktree (any exit code); pushes the
        │    task branch if the stage has push_branch and exit 0
        │  if the agent's auditor covers the stage → launch the auditor:
        │    same image, AUDIT_ONLY=1, its own run key, repos read-only,
        │    no MCP; it reads the run via /tasks/:id/brief?audit=<instance>
        │    and its verdict (feedback.md) decides what happens next
        │  RunStatus updated (idle / out_of_turns / failed / blocked)
        │  if AutoAdvance && exit_code==0 && no verdict says otherwise
        │    → move to next stage, or wait in awaiting_approval while
        │      the stage's approvals are unsettled
        ▼
Task advances (or stays) on board
```

## Data storage

All persistence is Postgres. The daemon opens **one** `*sql.DB` connection
pool against `OCTOPUS_DATABASE_URL` and hands it to every store; each store
runs its own namespaced set of migrations through `internal/migrate`, so
the schema is partitioned by component without splitting the connection.

Tables owned by each subsystem:

| Component | Tables |
|-----------|--------|
| `task` | tasks, task_comments, task_attachments, task_reports, task_questions, task_approvals, task_flags, workflow_events, task_dependencies |
| `project` | projects, environments |
| `auth` | users, workspaces, workspace_members, api_keys, refresh_tokens, oidc_identities, signup_allowlist |
| `audit` | audit_log |
| `usage` | sessions, session_tool_events, usage_daily, model_pricing |
| `secrets` | llm_keys (encrypted), llm_profiles, agent_llm_bindings |
| `settings` | settings |
| `sandbox` | task_sandboxes, sandbox_queue |
| `chat` | chat_messages |
| `webhook` | webhook_endpoints, webhook_deliveries, webhook_outbox |
| `automation` | automations, automation_runs, automation_run_steps, automation_fingerprints, automation_events |
| `entitlements` | workspace_entitlements |
| `registry` | agent_registry |

The local data directory keeps only file-tree artifacts: project worktrees
(`projects/<slug>/`), task attachments (`task-attachments/<task>/`), and
recent agent logs (`logs/`).

## Agent definitions

Agents live in Postgres, not on disk, in a catalog per workspace.
`internal/agentstore` holds three tables: `agents` (one row per definition:
stages, turns, granted skills, MCP grants, auditor spec), plus `content_files`
and `content_blobs` for the text — prompts, auditor instructions, skills and MCP
manuals. Each workspace owns its agents and skills under its own id as the
tenant. The system skills (`internal/builtin/system-skills/`, synced at boot
when the embedded `version` is higher) sit under a reserved tenant no workspace
can edit, the starter pack under another (empty, so a new workspace
starts with no agents and no skills), and the MCP manuals under the global
tenant, one per install. Content is addressed by sha256, so
cloning an agent copies pointers rather than bytes and identical prompts are
stored once no matter how many agents or tenants share them.

Everything that needs the agent list goes through `types.AgentSource`, cached
behind a cheap fingerprint (`count(*)`, `max(updated_at)`) because `All` sits on
the request path. A run gets its own definition, its granted skills plus the system
skills its stage's MCP servers bring, and the manual of each wired MCP through
`GET /run/bundle`, keyed by the task-scoped API key the dispatcher mints
per run — a pod cannot ask for another agent's bundle.

Agents are rows in Postgres, one set per workspace, edited from the Agents page
or the API. A directory of `agents/<name>/docker-compose.yml` plus prompt files
is an import format (`thulu agents import -workspace <id> <dir|file.json>`),
read by `types.Discover`.

## SSE event emission

Handlers in `internal/api/emit.go` publish server-sent events on task/project/agent mutations. The frontend pollers (`startPoller`) do REST polling rather than consuming SSE — the SSE endpoint at `/sandbox/sse` is reserved for the MCP sandbox protocol.
