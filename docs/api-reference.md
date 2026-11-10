# API Reference

Base URL: `http://localhost:8585` (or your configured domain).

All endpoints return `Content-Type: application/json`. Authenticated endpoints require either:
- `Authorization: Bearer <jwt>` header, or
- `X-API-Key: <key>` header (env `OCTOPUS_API_KEY` or a DB-issued key)

Auth roles in ascending order: `viewer` < `developer` < `admin` < `owner`.

---

## Public endpoints (no auth)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Full health check: daemon + all registered DBs |
| `GET` | `/health/live` | Liveness probe (always 200 if process is up) |
| `GET` | `/health/ready` | Readiness probe (200 only if all DBs healthy) |
| `GET` | `/openapi.json` | OpenAPI 3.0 spec (machine-readable) |
| `GET` | `/info` | Instance edition (`ce`/`ee`) and source URL; with a credential, also version and licensee |

`GET /metrics` serves Prometheus metrics and needs `OCTOPUS_METRICS_TOKEN` or an
admin of the operator workspace; see [metrics.md](metrics.md).

---

## Auth endpoints

Registered via `auth.Handler.RegisterRoutes()`.

There are no password, registration or MFA endpoints: identity belongs to the
configured identity provider and Thulu provisions an account on first
federated sign-in. See [auth.md](auth.md).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/auth/refresh` | — | Exchange refresh token → new access token |
| `POST` | `/auth/logout` | bearer | Revoke refresh token |
| `GET` | `/auth/me` | bearer | Current user info + workspace memberships |
| `GET` | `/auth/config` | — | Configured SSO providers, and whether any account exists yet |
| `GET` | `/auth/oidc/login` | — | Redirect to OIDC provider (state = HMAC-signed JSON) |
| `GET` | `/auth/oidc/callback` | — | OIDC callback → issues tokens |
| `GET` | `/auth/github/login` | — | Redirect to GitHub OAuth |
| `GET` | `/auth/github/callback` | — | GitHub OAuth callback → issues tokens |
| `GET` | `/auth/workspaces` | bearer | List workspaces the caller belongs to |
| `POST` | `/auth/workspaces` | bearer (admin) | Create workspace |
| `GET` | `/auth/workspaces/{id}/members` | bearer | List workspace members |
| `POST` | `/auth/workspaces/{id}/members` | bearer (admin) | Add a member, or invite an email with no account yet |
| `DELETE` | `/auth/workspaces/{id}/members/{userId}` | bearer (admin) | Remove member |
| `PATCH` | `/auth/workspaces/{id}/members/{userId}` | bearer (admin) | Update member role |
| `GET` | `/auth/workspaces/{id}/invitations` | bearer | List pending invitations |
| `PATCH` | `/auth/workspaces/{id}/invitations/{invitationId}` | bearer (admin) | Change an invitation's role |
| `DELETE` | `/auth/workspaces/{id}/invitations/{invitationId}` | bearer (admin) | Cancel an invitation |
| `GET` | `/auth/api-keys` | bearer | List the calling workspace's API keys |
| `POST` | `/auth/api-keys` | bearer | Create an API key: `role` + `permissions` required, returned once in plaintext |
| `DELETE` | `/auth/api-keys/{id}` | bearer | Revoke one of this workspace's keys (another's answers 404) |

The three key routes take an admin session, a key holding `keys:write`, or the
dev session of a no-login install. The dev session has no workspace of its
own: its keys go to the one named with `?workspace=`, or to the install's only
workspace. With two or more and none named it gets `400`.

---

## Agent types

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/types` | viewer | The caller's workspace catalog: every agent definition |
| `POST` | `/types` | admin | Create an agent in the caller's workspace, blank or cloned (`{name, template}`). A name that belonged to a deleted agent is free: the new agent takes it over, and the old agent's prompts are deleted (they stay in the history) |
| `DELETE` | `/types/{name}` | admin | Delete an agent from the caller's workspace. The row is disabled rather than removed, so tasks that named it keep the name; a new agent with that name is what those tasks run from then on |
| `GET` | `/types/{name}/files` | viewer | List the agent's stored files |
| `GET` | `/types/{name}/files/{path...}` | viewer | Read one of the agent's files |
| `PUT` | `/types/{name}/files/{path...}` | admin | Write one of the agent's files (body is the text) |
| `DELETE` | `/types/{name}/files/{path...}` | admin | Delete one of the agent's files |
| `GET` | `/types/{name}/settings` | viewer | Read agent-level settings (env overrides). `unknown_stages` lists per-stage config pointing at stages the board no longer has. `system_skills` lists the install's system skills (`name`, `description`, `mcp`, `enforcement`); `system_skills_by_stage` gives, for each valid stage, every system skill with `wired` (the stage has its MCP, or it needs none) and `enabled` (not turned off for this agent). A stage receives a system skill when both are true; a skill with a `when` (`token_optimizer`) also waits for that condition. `token_optimizer` is the agent's policy: `{level, engine, model}`, `level` `off` when it has none (see [Token optimizer](#token-optimizer)) |
| `PUT` | `/types/{name}/settings` | admin | Write agent-level settings. `system_skills_off` (`{stage: [skill, …]}`) turns system skills off for this agent on those stages, `octopus-run` included. Leaving it out or sending `null` keeps the stored value; `{}` clears it. Unknown names are kept and ignored. `token_optimizer` follows the same rule: left out or `null` keeps the stored policy. `level` must be `off`, `suggest` or `enforce`; `off` (or `""`) stores nothing, whatever `engine` and `model` say. On, it needs a registered `engine` and a `model`; otherwise 400 (`tokenopt.errors.invalid_level`, `engine_required`, `unknown_engine`, `model_required`). `system_skills` and `system_skills_by_stage` are read-only. The response is the settings as stored |
| `POST` | `/types/{name}/settings/prune-stages` | admin | Drop every reference to a deleted stage: labels + `prompts/<stage>.md` + `auditor/<stage>.md`. Returns `{stages, files}` removed |
| `GET` | `/types/{name}/prompts` | viewer | List stage prompt files for the agent |
| `GET` | `/types/{name}/usage` | viewer | Where the agent is assigned in the caller's workspace: `tasks` (how many tasks name it for some stage) and `projects` (`id`, `name`, `stages` of each project whose stage template names it) |
| `GET` | `/types/{name}/revisions` | viewer | History of one agent's text (newest first); `?path=` narrows to one file. `?path=.settings` is the history of its settings: each save records the settings as JSON, and a save that changed nothing records nothing. Those revisions are read and loaded into the form, not restored here |
| `GET` | `/types/{name}/revisions/{id}/body` | viewer | Bytes one revision produced. 404 unless the revision belongs to `{name}` in the caller's workspace |
| `POST` | `/types/{name}/revisions/{id}/restore` | admin | Roll one file back. `?mode=revert` (default) or `reset` |

Every route reads and writes the caller's own workspace; another workspace's
agent answers `404`. See
[admin-guide.md](admin-guide.md#editing-the-agent-catalog).

---

## Boards & stages

A **board** is a workflow: an ordered set of columns with their own capability
flags. Several can coexist — `feature`, `bugfix`, `research` — and a task
carries the board it runs on for life (`task.board_id`). The same stage id can
appear on several boards and behave differently on each: the label and colour
are shared, the flags are not.

### Boards

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/boards` | viewer | `{boards, default}` — the caller's workspace's flows with `stage_count` and `task_count`; `default` is that workspace's default board, seeded on the first call if it has none |
| `POST` | `/boards` | admin | Create a flow. Columns come from `stages` (explicit), `clone_from` (copy another board), or the built-in three (Backlog, In progress, Done). `default: true` also promotes it |
| `PATCH` | `/boards/{id}` | admin | Edit label/description (id is immutable). `default: true` moves the workspace's default marker |
| `DELETE` | `/boards/{id}` | admin | Delete a flow. 409 with `task_count` if it holds tasks — retry with `?move_to=<board>` (each task keeps its stage if the destination has it, else lands on that board's initial stage; optional `&stage=` overrides the fallback). A workspace's default board is protected. A board of another workspace answers 404 |
| `GET` | `/boards/{id}/stages` | viewer | One board's columns in flow order (`?include_virtual=1` appends mention/chat) |

### Stages

`GET /stages` serves the **catalog** — every stage used by a board the caller
can see. That is what
the stage-keyed configuration surfaces need (agent prompts, project hooks,
sandbox queue weights), because those are shared across boards. The columns of
a specific board come from `GET /boards/{id}/stages`.

The mutation endpoints act on **one board**, named by `?board=<id>` and
defaulting to the caller's workspace's default board. A `?board=` naming
another workspace's flow answers 404.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/stages` | viewer | The catalog (`?include_virtual=1` appends mention/chat; `?board=<id>` narrows to one board) |
| `POST` | `/stages?board=` | admin | Add a stage to the board. Body: StageInfo + optional `index` (slot; default: before the first terminal). Reusing an existing id adds that stage to this board. Returns the new board |
| `PATCH` | `/stages/{id}?board=` | admin | Edit flags on this board; label/colour edits are global. Setting `initial: true` moves the initial pointer atomically |
| `PUT` | `/stages/order?board=` | admin | Reorder: `{"ids": [...]}` must be an exact permutation of that board |
| `DELETE` | `/stages/{id}?board=` | admin | Remove a stage from the board. 409 with `task_count` if it holds tasks — retry with `&move_to=<stage>` to relocate them (no agents dispatched). The initial stage and the last terminal are protected. A stage removed from its last board leaves the catalog |

Every write validates the board invariants (exactly one `initial`, ≥1
`terminal`, slug ids, flag coherence) and emits a `stages.changed` webhook plus
a `stage.*` audit entry; board-level changes emit `boards.changed` and
`board.*`. Board ids follow the same slug rule as stage ids
(`^[a-z0-9_]{1,32}$`, no hyphens).

### Task types

A **task type** is a named subset of one board's flow: the same columns, minus
the ones it skips. `bugfix` going from Backlog straight to In progress and
`feature` walking the whole board are two types of one board — same screen, one
WIP count, an icon on the card telling them apart. A type never changes what a
column *means*; that stays the board's job, which is why a type can only remove
stages, and never the initial one or a terminal one.

`task.type_id` carries it. Empty means **untyped**: the full board, and a
first-class value — every task is untyped until somebody defines a type.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/task-types` | viewer | The catalog — every type any board offers |
| `GET` | `/task-types/icons` | viewer | The icon allowlist (`{icons: [...]}`); a type's `icon` must be one of these |
| `GET` | `/boards/{id}/types` | viewer | One board's types in display order, with `skips`, `default` and `task_count`, plus the board's `untyped` count |
| `PUT` | `/boards/{id}/types` | admin | Replace the whole list: `{"types": [...]}`. Creating, renaming, reordering, re-defaulting and changing skips are all this one call |
| `GET` | `/boards/{id}/stages?type=<id>` | viewer | The columns that type actually walks — "where can this task go", as opposed to what the board renders |

`PUT` refuses any edit that would strand tasks, with the destination it needs:

- a type that disappears while it still holds tasks → 409 with `task_count`;
  retry with `?move_to=<type>` (`?move_to=` with an empty value means untyped)
- a type that gains a skip on a column its tasks are sitting in → 409 with
  `task_count` and `available_stages`; retry with `?move_to_stage=<stage>`

Relocations are administrative: the task is updated, a timeline event is
recorded and `task.stage_changed` fires, but no agent is dispatched. The write
emits `task_types.changed` and a `board.task_types_changed` audit entry. Type
ids follow the same slug rule as stages and boards (`^[a-z0-9_]{1,32}$`), at
most 16 per board.

### Typing a task

`POST /tasks` accepts `type_id`; without one the task gets the board's default
type, or untyped if the board has none. A `type_id` the board does not offer is
a 400 — at creation the caller asked for something specific, and silently
running the whole flow instead is a surprise that only surfaces three columns
later. A subtask inherits its parent's board and type.

`PATCH /tasks/{id}` accepts `type_id`. If the task's current stage is one the
new type skips, the change is refused with 409 and the new type's
`available_stages`; retry with `stage` set in the same patch. Moving a task to a
stage its own type skips is refused the same way — the column exists on the
board, but not in this task's flow.

Moving a task to a board that does not offer its type drops it to untyped
rather than refusing: untyped always exists and leaves the task fully
functional, while a stage the destination lacks would leave it inert. The two
are not symmetric, so they are not treated the same.

### Moving a task between boards

`PATCH /tasks/{id}` accepts `board_id`. If the task's current stage doesn't
exist on the destination the move is refused with 409 and the destination's
`available_stages`; retry with `stage` set in the same patch. A task parked in
a column its board doesn't have would be inert — no gates, no auto-advance, no
agent — so the API doesn't let you create one.

`GET /tasks?board=<id>` filters by flow; prefer it over `?stage=`, which can
match tasks from several boards at once.

### Listing and paginating

Tasks in a list carry no `body` — the board's cards never render it. Fetch
`GET /tasks/{id}` for a task's markdown.

`GET /tasks`, `GET /usage/sessions` and `GET /audit` return everything matching
when called with no pagination params. Pass `?limit=` (capped at 500; `/audit`
keeps its own 1000) for a page: while more rows remain the response carries an
`X-Next-Cursor` header, and that token goes back as `?cursor=`. Page until the
header stops appearing — a page can come back short and still have a successor,
because the workspace and subtask filters run after the rows are read. The
cursor is opaque and names a row, not a position, so rows reordered between
pages are neither skipped nor repeated.

### Orphaned per-stage config

Removing a stage leaves its per-stage configuration alone — agent labels, agent
prompts and project stage files survive, so recreating the id brings them back.
Both are reported (`unknown_stages` on agent settings, `orphans` on the project
stage-file listing) and cleaned up on demand through the `prune` endpoints
above. A stage that still lives on another board is not an orphan.

---

## Skills & tools

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/skills` | viewer | The workspace's skills, then the system skills (`system: true`, with their `mcp` and `version`) |
| `POST` | `/skills` | admin | Create a workspace skill (`{name, description}`): writes `skill.yaml` and `prompt.md`. `409` for a taken name, a system skill's name, or the `octopus-` prefix |
| `DELETE` | `/skills/{name}` | admin | Delete a workspace skill |
| `GET` | `/skills/{name}/files` | viewer | List a workspace skill's files |
| `GET` | `/skills/{name}/files/{path...}` | viewer | Read one file |
| `PUT` | `/skills/{name}/files/{path...}` | admin | Write one file (body is the text) |
| `DELETE` | `/skills/{name}/files/{path...}` | admin | Delete one file |
| `GET` | `/skills/{name}/revisions` | viewer | History of the skill's files; `?path=` narrows to one |
| `GET` | `/skills/{name}/revisions/{id}/body` | viewer | Bytes one revision produced |
| `POST` | `/skills/{name}/revisions/{id}/restore` | admin | Roll one file back. `?mode=revert` (default) or `reset` |
| `GET` | `/system/skills` | viewer | The system skills |
| `GET` | `/system/skills/{name}/files[/{path...}]` | viewer | A system skill's files, or one file |
| `PUT` | `/system/skills/{name}` | admin + operator | Hotfix: replace the skill with `{files: {path: text}}`, keeping its stored version |
| `PUT` | `/system/skills/{name}/files/{path...}` | admin + operator | Hotfix one file |
| `DELETE` | `/system/skills/{name}` | admin + operator | Remove a system skill; one the binary carries comes back at the next boot |
| `GET` | `/tools` | viewer | List MCP tools available to agents. The per-tool list is filled in by probing each live server, which only runs for admin callers — everyone else gets server names without the tool breakdown |

---

## Agents (running containers)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/agents` | viewer | List all known agent containers |
| `GET` | `/agents/{id}` | viewer | Get single agent (Docker inspect data + registry state) |
| `POST` | `/agents/{id}/stop` | developer | Stop a running agent container |
| `DELETE` | `/agents/{id}` | admin | Remove agent container |
| `GET` | `/agents/{id}/files` | viewer | List output files produced by the agent |
| `GET` | `/agents/{id}/files/{path...}` | viewer | Download an agent output file |
| `GET` | `/agents/{id}/logs` | viewer | Stream container logs (chunked) |

Every route here is scoped to the caller's workspace: the list omits other
tenants' runs and the single-run routes answer `404` for them. A run with no
workspace on it is visible only to an unrestricted caller. `output_dir` is
an absolute path on the daemon host and is likewise operator-only.

---

## Usage

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/usage` | viewer | Aggregated token/cost stats |
| `GET` | `/usage/sessions` | viewer | List the caller's usage sessions (one per agent run) |
| `GET` | `/usage/sessions/{id}/logs` | viewer | Full JSONL log for a session |
| `GET` | `/usage/sessions/{id}/logs/stream` | viewer | Stream JSONL log (SSE) |
| `GET` | `/usage/sessions/{id}/files` | viewer | List files produced in a session |
| `GET` | `/usage/sessions/{id}/files/{path...}` | viewer | Download a session file |
| `GET` | `/usage/sessions/{id}/breakdown` | viewer | Where the run's cost went: `categories`, every tool call with its cost (`tools`) and the API calls (`calls`). See below |
| `GET` | `/usage/by-tool` · `/usage/by-skill` | viewer | Cost per tool and per skill; `cost_usd` is the attributed cost |
| `GET` | `/usage/by-mcp-server` | viewer | Calls, result tokens and cost per MCP server |
| `GET` | `/usage/token-optimizer` | viewer | Runs per Token optimizer level (`off`, `suggest`, `enforce`): count, cost, average, tools and delegations |
| `GET` | `/usage/delegations` | viewer | The Token optimizer's calls to its worker: `summary`, `by_agent`, `recent` (`?limit=`, default 25) |

Every route here reports on the caller's workspace only, aggregates included: a
session belongs to the workspace of the task it ran for, and one belonging to
another workspace answers 404 rather than 403, so run ids can't be probed for.
Runs the daemon could not tie to a task or a project carry no workspace and are visible only to an
unrestricted caller (the operator key, a dev-mode loopback session). `/usage/pricing`
is the exception: model prices are install-wide configuration, not run data.

### Cost attribution

Every Claude Code run is split by where its cost went
(`internal/usage/attribution.go`). Each API call's prompt is walked from the
start: the cached prefix is charged to the oldest content at the cache-read
price, the rest to the newest at the write price, and what entered the prompt
between two calls is split by bytes among the tool results, the model's own
output and messages sent to the run. A subagent's calls go to the `Agent` call
that started it. The shares are scaled to the reported cost, so they add up to
it; what the stream cannot explain is `unattributed`.

Categories: `startup` (system prompt, tool schemas, the brief), `reasoning`,
`user_input`, `tools`, `subagents`, `compaction`, `delegation` (the Token
optimizer's worker, billed apart and added on top of the run's cost) and
`unattributed`. A tool row carries `result_tokens` (what it added to the
prompt), `carried_calls` (how many calls read it again), `args_tokens`,
`cost_usd`, `delegation_id`/`delegated_cost_usd` and `input_summary` (the file,
command or query). `/tasks/{id}/usage` adds `breakdown` (categories, the ten
most expensive calls, delegations) once at least one of the task's runs is
attributed. Runs of other engines have no per-call usage and stay `unattributed`.

### Token optimizer

A run of an agent with the token optimizer on hands bulk file reads to a
cheaper "worker" model, and reads the worker's answer instead of the files.
The policy is the agent's (`token_optimizer` in its settings):

```json
{"level":"suggest","engine":"claude-code","model":"claude-haiku-4-5"}
```

`level` is `off`, `suggest` or `enforce`; `engine` and `model` are the worker.
The worker answers with the workspace's default LLM profile for `engine`, the
profile an agent on that engine runs on. An `api` worker is called by the
daemon (`POST /internal/token-optimizer/delegate`); a `cli` worker runs in the
run's own pod (`prepare`, then `finish`; see
[Token optimizer delegation](#token-optimizer-delegation)).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/token-optimizer/workers` | viewer | One entry per registered engine: whether the workspace's default profile for it can back a worker, and how a run reaches it. The agent form offers the available ones |
| `GET` | `/token-optimizer/resolve?agent=` | viewer | What a run of `agent` gets. Without `agent` it answers `off`; an unknown agent is 404 |

```json
{"workers":[
 {"engine":"claude-code","available":true,"via":"cli","provider":"anthropic","profile_name":"Claude Max","reason":""},
 {"engine":"codex","available":false,"via":"","provider":"","profile_name":"","reason":"no_default_profile"}]}
```

For the `codex` entry above see [engines.md](engines.md). The default profile's credential decides `via`:

- `cli`: the `claude-code` engine's default profile holds a Claude
  subscription OAuth token (`sk-ant-oat…`). That token may only go to the
  Claude Code client, so the daemon never sends it to an API: the run's own
  pod runs `claude -p` with the model and that token. Only Claude Code's agent
  image runs that CLI (it installs it on first use, see
  [enterprise-setup.md](enterprise-setup.md#claude-code-in-the-agent-images)),
  so this works for agents on `claude-code` only.
- `api`: a profile that speaks `anthropic`, `openai` or `openai-compatible`
  and holds a key the daemon may send to that API. The key never reaches the
  pod.

Anything else (no default profile: `no_default_profile`; no key, Bedrock or
Vertex, a federated profile, a Claude subscription token on another engine's
profile: `unsupported_credential`) leaves the worker unavailable. The entry
shows the profile's name and provider, never its key.

The level a run gets: the agent's. A policy without engine or model, or whose
worker is unavailable, is `off` (reason `no_worker`); so is a `cli` worker for
an agent whose engine is not `claude-code`, since the worker's `claude` runs in
the run's own container and only Claude Code's image ships it. `enforce` on an agent
whose engine is not `claude-code` becomes `suggest` (reason
`engine_without_hooks`): enforcing needs the engine's hooks. The threshold is
fixed at 24576 bytes.

```json
{"level":"suggest","threshold_bytes":24576,"reason":"","read_engine":"claude-code","read_via":"api","read_model":"claude-haiku-4-5"}
```

When the level is not `off`, the run's env gets
`OCTOPUS_TOKEN_OPTIMIZER=<level>`, `OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES=24576`
and `OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=api|cli`. A `cli` worker also passes
`OCTOPUS_TOKEN_OPTIMIZER_READ_MODEL` and
`OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN`, the default profile's
subscription token, in the pod's env Secret (Kubernetes) or the container's
env (Docker), for the worker's `claude -p`. An `api` worker's run gets no credential. The
`octopus-token-optimizer` system skill reaches the run. With `off` none of it
does. Stage runs, @mentions and chats all resolve this way, in Docker and
Kubernetes alike.

---

## Projects

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/projects` | viewer | List all projects |
| `POST` | `/projects` | admin | Create project (clone remote repo) |
| `POST` | `/projects/upload` | admin | Create project from an uploaded folder (multipart `meta` then `archive`). See [projects.md](projects.md#upload-a-folder) |
| `POST` | `/projects/{id}/upload` | developer | New version of an uploaded project's folder, committed on the default branch |
| `GET` | `/projects/{id}/branches` | viewer | Branches of an uploaded project |
| `GET` | `/projects/{id}/archive` | viewer | Zip of a branch of an uploaded project (`?branch=`) |
| `GET` | `/projects/{id}` | viewer | Get project details |
| `PATCH` | `/projects/{id}` | admin | Update project metadata |
| `DELETE` | `/projects/{id}` | admin | Delete project (removes clone + worktrees) |
| `GET` | `/projects/{id}/worktrees` | viewer | List active worktrees for a project |
| `GET` | `/projects/{id}/metrics` | viewer | Flow + agent-spend metrics for one project. Accepts `?from=&to=` (RFC3339, default: last 30 days). See below |
| `GET` | `/projects/{id}/stage-files` | viewer | List per-stage sidecar files (prompts, hooks); `orphans` lists files whose stage is gone |
| `POST` | `/projects/{id}/stage-files/prune` | admin | Delete those orphaned files. Returns `{stages, files}` removed |
| `GET` | `/projects/{id}/stage-files/{kind}/{stage}` | viewer | Read a stage file |
| `PUT` | `/projects/{id}/stage-files/{kind}/{stage}` | admin | Write a stage file |
| `DELETE` | `/projects/{id}/stage-files/{kind}/{stage}` | admin | Delete a stage file |

### Environments

Where a project's repo actually runs. Naming the deployments is what lets an
agent (or a dashboard) ask about *prod's* error rate rather than the project's
in the abstract — and keeps a dev blip from reading like an incident.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/projects/{id}/environments` | viewer | List environments; each carries `has_credential` |
| `POST` | `/projects/{id}/environments` | admin | Create one. `name` is lowercased and unique per project |
| `PATCH` | `/projects/{id}/environments/{envId}` | admin | Partial update: omitted fields keep their value, `""` clears one |
| `DELETE` | `/projects/{id}/environments/{envId}` | admin | Delete it *and* its stored credential |
| `GET` | `/projects/{id}/environments/{envId}/credential` | viewer | Status only: `kind` + masked `hint`, never the payload |
| `PUT` | `/projects/{id}/environments/{envId}/credential` | admin | Store `{kind, payload}`. Encrypted at rest |
| `DELETE` | `/projects/{id}/environments/{envId}/credential` | admin | Remove the credential |
| `GET` | `/internal/environments/{envId}/credential` | developer | **The only route that returns plaintext.** For MCP sidecars |

### Metric mappings

Where each dashboard signal comes from *in your cloud account*. The
dashboard is fixed — that is what makes projects comparable — and the mapping is
the part discovered per account, usually by an agent correlating a cloud
inventory with the repo's deploy config.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/env-signals` | viewer | The closed signal catalog |
| `GET` | `/projects/{id}/environments/{envId}/mappings` | viewer | Mappings, proposals first |
| `POST` | `/projects/{id}/environments/{envId}/mappings` | developer | Propose a batch. Body: `{mappings, task_id?, flag_id?}`. With `task_id` and no `flag_id`, the daemon adds a review approval to that task; `flag_id` names an existing approval of the task instead |
| `POST` | `/projects/{id}/environments/{envId}/mappings/approve` | admin | Promote every proposal to active, retire what they replace, approve the review approvals the proposals added |
| `DELETE` | `/projects/{id}/environments/{envId}/mappings/{mapId}` | admin | Remove one mapping |
| `POST` | `/projects/{id}/environments/{envId}/collect` | developer | Poll this environment now instead of waiting for the tick. Throttled per environment — each call is billed to your cloud account |
| `GET` | `/projects/{id}/environments/{envId}/series` | viewer | Collected points. `?signal=&service=&from=&to=` (default: last 24h) |

Signals are a closed set: `latency_p50`, `latency_p95`, `error_rate`,
`throughput`, `cpu`, `memory`, `restarts`, `cost_daily`, `saturation`. Closed on purpose —
cardinality is the only thing that can break continuous collection, and every
signal here earns its place by pointing at something someone can act on.

```jsonc
{
  "id": "…", "environment_id": "…",
  "service": "api",              // service granularity, never per-endpoint
  "project_id": "…",             // which repo owns this service (multi-repo routing)
  "signal": "latency_p95",
  "source": {                    // provider-shaped; `unit` is load-bearing
    "namespace": "AWS/ApplicationELB",
    "metric_name": "TargetResponseTime",
    "dimensions": { "LoadBalancer": "app/api-prod/abc123" },
    "stat": "p95",
    "unit": "s"                  // an ALB reports seconds, Lambda milliseconds
  },
  "status": "proposed",          // proposed | active | disabled
  "created_by": "agent:ops"
}
```

**A proposal is inert.** Only `active` mappings are polled, so there is no
window in which something unreviewed feeds the panel. `POST …/approve` is the
human gate; it also approves the review approval the proposal added to the task,
so approving is one action rather than two screens.

Collection cadence is the `envmetrics.interval_seconds` setting (default 300;
`0` disables it). Every poll costs **your account** a CloudWatch request, which
is why it is a setting and not a constant. Points are 5-minute gauges — last
value wins per bucket, kept 90 days.

```jsonc
{
  "id": "…", "project_id": "…",
  "name": "prod",              // lowercase, [a-z0-9._-], unique per project
  "provider": "aws",           // aws | gcp | other
  "region": "us-east-1",
  "account": "123456789012",   // AWS account id or GCP project id
  "url": "https://app.example.com",
  "selectors": { "tag:Environment": "prod" },
  "is_production": true,       // turns on the stricter reading of every signal
  "has_credential": true       // derived per request, never stored
}
```

Credential `kind` decides the payload shape:

| Kind | Payload keys |
|---|---|
| `aws_keys` | `access_key_id`, `secret_access_key`, optional `session_token` |
| `aws_role` | `role_arn` |
| `gcp_sa` | `service_account_json` |

The payload is **write-only over the caller-facing API**: it is encrypted with
its own HKDF label and the status route returns only a masked hint. This is the
same seam `thulu-github` uses for PATs — an MCP server resolves the credential
server-side, so a cloud key never enters a prompt. Use a read-only identity
regardless.

### `GET /projects/{id}/metrics`

Powers the project detail page (`/project/?id=…`). Joins the two halves of the
story that live in different stores: how work moves through the board (`flow`,
from `tasks` + `workflow_events`) and what the agents spent moving it (`agent`,
from usage `sessions`).

A `project_id` in the query string is ignored — the path decides which project is
described, so the response can never widen past what the caller was authorized
for.

**Window semantics.** Rate-shaped fields (`throughput`, `lead_time`,
`stage_dwell`, rework, `agent_runs`) are computed over `[from, to]`.
Snapshot-shaped fields (`by_stage`, `wip_age`, and the `tasks_*` counts other
than `tasks_created` / `tasks_completed`) always describe *now*.

```jsonc
{
  "project_id": "…", "from": "…", "to": "…",
  "flow": {
    "tasks_total": 42, "tasks_open": 7, "tasks_done": 35,
    "tasks_blocked": 1, "tasks_running": 2, "tasks_failed": 0,
    "tasks_created": 9, "tasks_completed": 8, "tasks_touched": 19,
    "by_stage":    [{ "stage": "development", "count": 4 }],
    "throughput":  [{ "bucket": "2026-07-30", "completed": 2, "created": 1 }],
    "lead_time":   { "n": 8, "avg_ms": 0, "p50_ms": 0, "p90_ms": 0, "max_ms": 0 },
    "stage_dwell": [{ "stage": "review", "n": 12, "p50_ms": 0, "p90_ms": 0 }],
    "wip_age":     { "n": 7, "p90_ms": 0 },
    "reworked_tasks": 2, "rework_rate": 0.22, "kickbacks": 3,
    "first_pass_tasks": 6, "first_pass_rate": 0.75,
    "human_comments": 4, "human_intervention_rate": 0.21,
    "agent_runs": 27, "runs_per_task": 1.4,
    "flags_raised": 1, "flags_resolved": 1, "prs_opened": 8
  },
  "agent": {
    // …plus every field of the /usage/efficiency payload, inlined
    "tasks_with_runs": 19,
    "cost_per_task": 0.42, "cost_per_completed_task": 0.99,
    "tokens_per_task": 91000, "sessions_per_task": 1.4,
    "compute_time_per_task": { "n": 19, "p50_ms": 0 },
    "wall_time_per_task":    { "n": 19, "p50_ms": 0 }
  }
}
```

Notes on the numbers themselves:

- `stage_dwell` counts only **closed** intervals — a task still sitting in a
  stage is censored data and would understate the true dwell. It shows up in
  `wip_age` instead.
- Per-task rates divide by tasks that saw activity in the window, not by every
  task ever linked to the project.
- `compute_time_per_task` sums each task's agent runtime; `wall_time_per_task`
  spans its first start to its last finish. The two diverge exactly where the
  pipeline queues rather than works.
- `first_pass_rate` is stricter than `1 - rework_rate` on purpose. The
  *completion* must fall inside the window, but a kickback disqualifies the task
  whenever it happened — otherwise work sent back last month and finished today
  would score as a clean first pass.
- `human_intervention_rate` counts comments whose source is `user`, per touched
  task. Agent, auditor and system comments are the pipeline talking to itself.
- `cost_per_completed_task` divides the same spend by tasks that actually
  shipped. The gap between it and `cost_per_task` is money going into work that
  hasn't landed.

---

## Tasks

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/tasks` | viewer | List all tasks (supports `?stage=` and `?board=` filters). Top-level tasks only unless `?include_subtasks=true`; `?parent_id=` lists one parent's subtasks. A subtask row carries `sort_order`, its place in the parent's reading order (0 until someone reorders) |
| `POST` | `/tasks` | developer | Create task. Body: `title` (required), `body`, `project_ids`, `stage`, `board_id`, `type_id`, `stage_assignments`, `tags`, `priority`. Unknown fields are ignored: a misspelled `project_ids` creates a task with no project |
| `GET` | `/tasks/{id}` | viewer | Get task with comments, attachments, approvals (also as `flags`), test cases, questions and subtasks |
| `PATCH` | `/tasks/{id}` | developer | Update task (move stage, edit title/body, reassign agents) |
| `DELETE` | `/tasks/{id}` | admin | Delete task and all its data |
| `POST` | `/tasks/{id}/clone` | developer | Clone task (same stage, fresh worktrees) |
| `POST` | `/tasks/{id}/fork` | developer | Fork task (copies all sub-collections + files) |
| `POST` | `/tasks/{id}/relaunch` | developer | Re-run agent on current stage |
| `POST` | `/tasks/{id}/ensure-pr` | developer | Ensure a GitHub PR exists for this task |
| `POST` | `/tasks/{id}/subtasks` | developer | Create a subtask: `{title, body?, stage?, depends_on?, stage_assignments?, tags?, priority?}`. Inherits the parent's projects, board, type and stage agents. A subtask of a subtask is 409 |
| `PUT` | `/tasks/{id}/subtasks/order` | developer | Set the reading order of the subtasks: `{"ids": [...]}` naming every subtask once. An id that is not a subtask, or a list that leaves one out or repeats it, is 400 with `code` and `offending`. Returns `{children}` in the new order. Display order only; `depends_on` decides what runs first |
| `POST` | `/tasks/{id}/comments` | developer | Add a comment. `parent_id` answers that comment: the reply joins its thread (one level deep, so answering a reply answers its root). A parent that is not a comment of the task is 400. A person's reply reopens a resolved thread. The author comes from the credential |
| `POST` | `/tasks/{id}/threads/{threadId}/apply` | developer | Hand the thread (by its root's id) to the stage's agent, the way `/converse` does: into the live run, queued behind it, or as a continuation of the last session. Optional `{"note": "..."}` goes first. Returns the delivery, and notes it in the thread. Not with a run's key |
| `POST` | `/tasks/{id}/threads/{threadId}/resolve` | developer | `{"resolved": true}` closes the thread, `false` reopens it. Only a root; a reply id is 404. The root carries `resolved_at` and `resolved_by`. Not with a run's key |
| `POST` | `/tasks/{id}/attachments` | developer | Upload a file attachment |
| `GET` | `/tasks/{id}/brief` | viewer | Get the AI-generated task brief. Comments come grouped by thread, each showing its latest 6 replies (a resolved one only its last); `?thread=<root id>` shows that thread whole. `?audit=<instance>` appends what that run reported, for the auditor judging it |
| `POST` | `/tasks/{id}/auditor-feedback` | developer | Raw `feedback.md` (verdict frontmatter) for `?instance=<auditor run>`. Only the auditor's own run key, for its task and stage; anything else is 403/400 |
| `GET` | `/tasks/{id}/reports` | viewer | List long-form reports (agent index, auditor feedback, test evidence). Bodies are markdown |
| `POST` | `/tasks/{id}/reports` | developer | JSON `{kind, title?, body, instance_id?}` → 201 with the report. `kind` is `evidence` (anyone who can write the task) or `agent_index` (an agent run only, else 403). Body ≤ 256 KB (413), title ≤ 200 characters, `instance_id` must be a run of this task. A report notifies no one and launches no agent |
| `POST` | `/tasks/{id}/questions` | developer | `task_ask`: `{kind, blocking, context, questions: [{header, question, options: [{label, description}], multi_select, allow_other}], default}`. Only the key of the task's live stage run, on its own task and stage (403/409 otherwise); a continuation run (a chat message to a finished run) gets 409. At most 3 questions per run and 5 open per task (429). Posts the question as a comment and answers `{question, comment_id, instruction}` |
| `POST` | `/tasks/{id}/questions/{qid}/answer` | developer | `{answers: [...], note}`. A blocking question's answer reaches the live run, or resumes the stage when the run already ended. Not with a run's key |
| `POST` | `/tasks/{id}/questions/{qid}/cancel` | developer | Close an open question unanswered. Cancelling the last blocking one of a parked task sets it idle. Not with a run's key. Moving the task to another stage cancels its blocking questions on its own |
| `GET` | `/tasks/{id}/approvals` | viewer | List the task's approvals: the ones its stages require, the ones agents or people asked for, and approvals with no stage (`stage: ""`) |
| `POST` | `/tasks/{id}/approvals` | developer | Ask for an approval: `{label, key?, reason?, payload?}`, body ≤ 64 KiB. A run's key may ask on its own task, its group (parent, siblings, subtasks) or a task it created; agents may hold 10 pending per task (429 past that) |
| `POST` | `/tasks/{id}/approvals/{aid}/decide` | developer | `{decision, note}`: `approved`, `changes_requested` or `waived` (the last two need a note). Changes requested resumes the stage's agent, sends the task back, or leaves it, per the stage's `on_changes_requested`; during a live run they wait for its end. Not with a run's key |
| `DELETE` | `/tasks/{id}/approvals/{aid}` | developer | Remove an approval a person or agent opened. One a stage requires is 409: waive it instead. Not with a run's key |
| `GET` | `/inbox` | viewer | Pending questions and approvals of the caller's workspace, oldest first, at most 500 of each: `{questions, approvals}` |
| `PUT` | `/tasks/{id}/session-snapshot` | developer | `?sid=`: the engine's session as a tar.gz, ≤ 50 MiB (`OCTOPUS_SESSION_SNAPSHOT_MAX_BYTES`, 413 past it). Only with the key of the run it belongs to |
| `GET` | `/tasks/{id}/session-snapshot` | developer | `?sid=`: the saved session, for a resumed run to import. Only with a run's key, for its own agent and task |
| `GET` | `/tasks/{id}/conversation-transcript` | viewer | Markdown of the agent's earlier runs on the task (tools called, results) and what people told it, newest part kept within `OCTOPUS_TRANSCRIPT_MAX_BYTES` (200 KiB). A run reads its own agent's; a person names `?agent=`. An operator key needs `runs:read`. 404 when there is nothing |
| `GET` | `/tasks/{id}/flags` | viewer | Deprecated alias over approvals: lists them as flags |
| `POST` | `/tasks/{id}/flags` | developer | Deprecated: opens an approval of no stage |
| `POST` | `/tasks/{id}/flags/import` | developer | Raw `flags.yaml` body → one approval of no stage per entry, signed by the caller's credential. How agents raise flags when the runs dir isn't shared (Kubernetes) |
| `PATCH` | `/tasks/{id}/flags/{flagId}` | developer | Deprecated: `approved` and `rejected` are recorded like `/decide` (`rejected` is `changes_requested`, but resumes or moves nothing). Approving a mapping's review flag also activates the proposals behind it (`mappings_activated` in the response) |
| `DELETE` | `/tasks/{id}/flags/{flagId}` | developer | Deprecated: same rules as `DELETE /approvals/{aid}` |
| `GET` | `/tasks/{id}/test-cases` | viewer | List the task's test cases in order, each with its `runs` newest first |
| `POST` | `/tasks/{id}/test-cases` | developer | Add one case, or several as `{cases: [...]}`. Returns the created cases. A case without `key` gets `TC-<max+1>`; a taken key answers 409 |
| `POST` | `/tasks/{id}/test-cases/import-body` | developer | Turn the body's `## Test Cases` checklist into cases (`- [x]` lines come in as `pass`, covered); skips titles already present, so it may return `[]` |
| `PATCH` | `/tasks/{id}/test-cases/{case}` | developer | Partial update of `title`, `description`, `steps`, `expected`, `priority`, `status`, `covered`, `position`. `{case}` is the case id or its key (`TC-3`) |
| `DELETE` | `/tasks/{id}/test-cases/{case}` | developer | Delete the case and its runs |
| `POST` | `/tasks/{id}/test-cases/{case}/runs` | developer | Record a run (`verdict` pass/fail/blocked required; `observed`, `note`, `video_attachment_id`, `take_attachment_id`, `comment_id`, `capture_attachment_ids`). Every id must belong to the task (404 otherwise). The case takes the verdict as its status and counts as covered. Answers `{run, test_case}` |
| `GET` | `/test-cases` | viewer | QA view across the caller's workspace. Query: `status` (comma list), `board_id`, `project_id`, `q`, `uncovered=1`, `limit` (default 200, max 1000). Answers `{test_cases, counts, covered_counts}`; both count every status over the whole filtered set, the `status` filter left out, and `covered_counts` only the covered cases |
| `GET` | `/tasks/{id}/events` | viewer | List workflow events for this task |
| `GET` | `/tasks/{id}/usage` | viewer | Token/cost usage for this task |
| `GET` | `/tasks/{id}/dependencies` | viewer | List task dependencies (blockers) |
| `POST` | `/tasks/{id}/dependencies` | developer | Add a dependency (hard or soft) |
| `DELETE` | `/tasks/{id}/dependencies/{depId}` | developer | Remove a dependency |
| `GET` | `/task-attachments/{attachmentId}` | viewer | Download an attachment by ID |

Test cases are rows with their own history of runs; the model and the rules
are in [test-cases.md](test-cases.md). Nobody names the author of a case or a
run: it comes from the credential (`agent:<name>` for a run's key, `key:<name>`
for an operator key, the email for a session). A run's key may write the test
cases of the tasks it answers for, like any other change to a task.

### Who signs what

No request names its own author. Comments, attachments, approvals, section edits,
chat messages and messages in a task's conversation are signed with the author
of the credential that sent them, and any `author`, `source`, `created_by`,
`resolved_by` or `?agent=` in the request is ignored.

| Credential | Signs as | Comment source |
|------------|----------|----------------|
| Session (JWT) | the account's email, or `user:<id>` without one | `user` |
| Operator API key | `key:<key name>` | `user` |
| A run's key | the agent it was minted for (`agent:<name>` on approvals and proposals) | `agent` |
| A run's key minted without an agent | nothing: `403` | — |
| No credential, dev mode | `user` | `user` |

A `user` comment launches the agents it `@mentions`; a reply that names none
goes to the agent that spoke last in its thread. An `agent` comment launches the
other agents it names, until agents have written 3 times in a row in that thread
since a person last did; then the thread gets a notice and waits for a person.
No other source launches anyone. A run's key cannot apply or resolve a thread,
and cannot post to `/tasks/{id}/converse`, move a conversation message to the comments, or
post in a chat, its own included: those are where the person speaks.
`/conversation/{msg}/to-comment` moves only a message a person wrote, and the
comment keeps that message's signature. Agent names `user`, `system`, `auditor`
and `automation` are reserved for the daemon's own signatures.

---

## Sandboxes

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/sandboxes` | viewer | List all active sandboxes |
| `GET` | `/sandboxes/queue` | viewer | List pending sandbox acquisition requests |
| `DELETE` | `/tasks/{id}/sandbox` | admin | Force-release sandbox for a task |
| `GET` | `/tasks/{id}/sandbox` | viewer | The task's live sandbox (its parent's, for a subtask): status, idle time and port addresses. `204` with no body when it has none; `404` only when the task does not exist or is outside your workspace |
| `DELETE` | `/tasks/{id}/sandbox/queue` | developer | Take the task out of the sandbox line; a slot it had been granted passes to the next one. `404` when it is not in line |
| `GET` | `/sandbox/sse` | API key | MCP SSE endpoint (agents only — not for browsers) |
| `POST` | `/sandbox/messages` | API key | MCP JSON-RPC message endpoint |

---

## Settings

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/settings` | admin | List all host settings (key → JSON value) |
| `GET` | `/settings/{key}` | admin | Get a single setting |
| `PUT` | `/settings/{key}` | admin | Set a setting value. Unknown keys are rejected with `400` |
| `PATCH` | `/settings` | admin (operator) | Set several settings at once, `{key: value, …}`. Every value is validated first and all are written in one transaction, or none. A new `sandbox.priorities` reorders the tasks already waiting for a sandbox |

These are host-level knobs (container caps, eviction windows, poll intervals),
not per-workspace preferences — one table shared by the whole install.

### Workspace settings

The few install settings a workspace may choose for itself, inside bounds the
operator sets. Today only `agent.max_run_minutes`, the wall-clock limit on one
agent run, capped by the install's `agent.max_run_minutes_ceiling`.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/workspace-settings` | viewer | The caller's workspace's run limit, in minutes |
| `PUT` | `/workspace-settings/agent.max_run_minutes` | admin | Body: whole minutes from 1 to `ceiling`, or `null` to go back to the install default. Answers the GET's shape. Out of range or not a whole number is a `400` naming the bounds; any other key is the same `400` `/settings` gives an unknown key. Each change leaves an audit entry `workspace.setting_changed` |

```json
{"agent.max_run_minutes": {"value": 180, "default": 60, "ceiling": 240, "effective": 180}}
```

`value` is the workspace's own choice (`null` when it has none), `default` the
install's limit as the deployment reads it (`0` = no limit), `ceiling` the most
`value` may be, and `effective` what the workspace's runs get: `min(value,
ceiling)`, or `default` without a value. When the operator lowers the ceiling
below a stored value, `effective` follows the ceiling and the value stays.

---

## LLM keys, profiles & bindings

BYOK credentials and routing. Keys are encrypted at rest and never returned —
status endpoints only expose a masked hint.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/engines` | admin | List available engines + per-engine key status. `claude-code` (the main engine), `codex` and `opencode` |
| `GET` | `/llm-key` | admin | Key status for the workspace (`?engine=`, `?workspace=`) |
| `PUT` | `/llm-key` | admin | Store/replace the workspace BYOK key (`?workspace=`) |
| `DELETE` | `/llm-key` | admin | Remove the workspace BYOK key (`?workspace=`) |
| `GET` | `/llm-profiles` | admin | List LLM profiles (engine, model, key, base_url, org_id bundles) |
| `POST` | `/llm-profiles` | admin | Create a profile |
| `GET` | `/llm-profiles/{id}` | admin | Get a profile (key masked) |
| `PATCH` | `/llm-profiles/{id}` | admin | Update a profile |
| `DELETE` | `/llm-profiles/{id}` | admin | Delete a profile |
| `GET` | `/agents/{name}/llm-binding` | viewer | Which profile drives this agent |
| `PUT` | `/agents/{name}/llm-binding` | developer | Bind a profile (engine guardrail; admins force via `X-Force-Engine: true`) |
| `DELETE` | `/agents/{name}/llm-binding` | developer | Remove the binding (fall back to default) |
| `GET` | `/agents/{name}/effective-llm` | viewer | Resolved engine/model/key-source after binding + defaults |

`?workspace=` names the target workspace, for a caller that has none of its own
(dev mode without credentials). A key naming its own workspace is accepted, and
naming anyone else's is `403`. Every response carries the `workspace_id` it acted
on: check it rather than the status code.

---

## Credentials

Third-party keys (AWS, Google Cloud, GitHub) the MCP servers use on an agent's
behalf. Admin throughout — every route here either wraps a secret or decides who
can reach one. **No route ever returns a payload**: creating and rotating take
one in, nothing hands one back. Create and rotate validate against the provider
synchronously and record what it can actually do.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/credential-providers` | admin | Provider specs (kinds + form fields) and the credential-backed tool catalog |
| `GET` | `/credentials` | admin | List the workspace's credentials (state, identity, grants) |
| `POST` | `/credentials` | admin | Store a credential; validates and introspects it before returning |
| `GET` | `/credentials/decisions` | admin | Recent authorization outcomes — allows *and* denials |
| `GET` | `/credentials/{id}` | admin | One credential plus its per-tool matrix |
| `PATCH` | `/credentials/{id}` | admin | Rename and/or rotate the secret (rotating re-validates) |
| `DELETE` | `/credentials/{id}` | admin | Delete the credential, its grants and its tool policy |
| `POST` | `/credentials/{id}/validate` | admin | Re-check against the provider and re-read capabilities |
| `PUT` | `/credentials/{id}/disabled` | admin | Kill switch for the whole credential (keeps grants + policy) |
| `PUT` | `/credentials/{id}/grants` | admin | Replace which projects/environments can reach it |
| `PUT` | `/credentials/{id}/tools` | admin | Replace the per-tool policy (which tools it may drive) |

---

## Entitlements & workspaces

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/entitlements` | viewer | Workspace plan limits (seats, concurrency, run quota) |
| `PUT` | `/entitlements` | admin | Set the workspace plan |
| `DELETE` | `/workspaces/{id}` | admin | Right-to-erasure: delete workspace + all data (owner + slug confirmation enforced) |

---

## Webhooks

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/webhooks` | admin | List outbound webhooks |
| `POST` | `/webhooks` | admin | Create webhook. `400` when the URL is not http(s) or its host resolves to a loopback, private (unless in `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS`), link-local or cloud-metadata address |
| `POST` | `/webhooks/{id}/test` | admin | Send a test event to the webhook URL |
| `POST` | `/webhooks/{id}/rotate-secret` | admin | Replace the signing secret; the response carries the new one, shown once |
| `DELETE` | `/webhooks/{id}` | admin | Delete webhook |

Every delivery carries `X-Octopus-Signature: sha256=<hex>`, the HMAC-SHA256 of
the raw request body keyed with the endpoint's secret (the `whsec_…` value
returned on creation or rotation). Verify it with a constant-time compare:

```python
expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, request.headers["X-Octopus-Signature"])
```

Delivery URLs must resolve to public addresses. A receiver on an internal
network needs its range in `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS`.

Events: `task.created`, `task.stage_changed`, `task.deleted`, `chat.message`,
`boards.changed`, `stages.changed`, `task_types.changed`, `question.asked`,
`question.answered`, `question.canceled`, `approval.requested` and
`approval.decided`. The question and approval events carry the task id and
title, the question or approval, and who acted.

---

## Automations

Recipes that run on a schedule, on an event, or when a metric crosses a
threshold. The YAML in `spec` is the source of truth — the columns on the row
(`trigger_kind`, `cron`, `next_run_at`) are derived from it. See
[automations.md](automations.md).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/automations` | developer | List recipes + the event catalog a trigger can subscribe to |
| `POST` | `/automations` | admin | Create from `{"spec": "<yaml>", "enabled": true}`. `400` when the spec does not parse or names a board, stage, agent, project or environment the workspace does not have |
| `POST` | `/automations/validate` | admin | Parse a spec and check its references without storing it; answers `valid`, `parsed` (the recipe as JSON) and, for scheduled ones, `next_runs` |
| `GET` | `/automations/{id}` | developer | Get one recipe (includes the parsed spec) |
| `PUT` | `/automations/{id}` | admin | Replace the spec |
| `DELETE` | `/automations/{id}` | admin | Delete the recipe and its history |
| `PUT` | `/automations/{id}/enabled` | admin | Suspend / resume (`{"enabled": false}`) |
| `POST` | `/automations/{id}/run` | admin | Fire it by hand, optionally with `{"payload": {...}}` as `trigger.payload` (test data for an event or metric recipe); the concurrency gate still applies |
| `GET` | `/automations/{id}/runs` | developer | Run history (`?limit=`, default 50) |
| `GET` | `/automations/{id}/runs/{runId}` | developer | One run with its step-by-step timeline |
| `POST` | `/automations/{id}/runs/{runId}/cancel` | admin | Stop an in-flight run |

A run answers `skipped` with a `reason` when a guard rail stopped it — another
run is already going (`concurrency: forbid`), the firing was too late to be
worth catching up (`starting_deadline`), the event `filter` failed to evaluate,
the event comes from a chain of three recipes, or the day's
`max_agent_runs_per_day` is spent. Events inside a debounce window are held
and run once when it closes, with all of them in `trigger.payload.events`. A
run's `reason`, `error` and step outputs come back in the request's language.
Every id-addressed route answers `404` for an automation in another workspace.

---

## Audit log

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/audit` | admin | Query audit log (supports `?limit=`, `?before=`, `?kind=`) |
| `GET` | `/audit/export` | admin | Export the audit log |
| `GET` | `/audit/verify` | admin of `OCTOPUS_OPERATOR_WORKSPACE` | Recompute the hash chain; answers `ok`, `checked`, `legacy`, `broken_at`, `head` |

---

## Chats

Persistent conversations with chat-capable agents (those declaring the virtual
`chat` stage in `thulu.stages`). Posting a message is **async**: the daemon
returns `202` and runs the turn in a warm per-chat container; the reply arrives
via the SSE stream (or by re-fetching messages).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/chats` | developer | Create a chat (`agent_type` required; `task_id`, `title` optional) |
| `GET` | `/chats` | viewer | List chats (`?include_archived=true`) |
| `GET` | `/chats/{id}` | viewer | Get one chat (includes `status: idle\|running`) |
| `PATCH` | `/chats/{id}` | developer | Rename / archive (`{"title": …, "archived": …}`) |
| `DELETE` | `/chats/{id}` | developer | Delete chat + messages |
| `POST` | `/chats/{id}/reset` | developer | Wipe transcript + engine session (fresh start) |
| `GET` | `/chats/{id}/messages` | viewer | List messages (`?limit=`, `?before=<message_id>`) |
| `POST` | `/chats/{id}/messages` | developer | Post message → `202` (`409` while a turn is running) |
| `GET` | `/chats/events` | viewer | SSE stream: `message_added`, `turn_started/finished/failed`, `chat_*`, `action_pending/decided` |
| `POST` | `/chats/{id}/delegation` | developer | `{"enabled": true}` lets the chat act for its creator for 8 h; `false` ends it. See below |
| `GET` | `/chats/{id}/actions` | viewer | Deletions, merges and deployment reviews the delegated agent asked for (`?status=pending`) |
| `POST` | `/chats/{id}/actions/{actionId}/confirm` | developer | Run a held request as the person the chat acts for |
| `POST` | `/chats/{id}/actions/{actionId}/cancel` | developer | Turn a held request down |
| `GET` | `/chat-delegation` | viewer | Whether the workspace allows delegated chats (`{"enabled", "ttl_hours"}`) |
| `PUT` | `/chat-delegation` | owner | `{"enabled": false}` turns them off and ends every delegation in force |

### Personal preferences

The dashboard's per-person choices, kept with the account. A person only (the
dev session keeps its own): an API key gets 403. The daemon knows each key and
validates its value; an unknown key is 404 and a value over 4 KiB is 413.

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/me/preferences` | viewer | `{"preferences": {key: value}}`, only the keys this person set that still validate |
| `PUT` | `/me/preferences/{key}` | viewer | Replaces the whole value → `{"key", "value"}` with the defaults filled in |

`tasks.view` is the board page's layout: `{"layout": "board"|"list", "group":
"none"|"stage", "sort": ""|"title"|"stage"|"status"|"priority"|"updated",
"dir": "asc"|"desc"}`. `group` defaults to `none` and `dir` to `asc`; `sort`
`""` is the board's own order.

### Personal engine login

Docker installs only; a Kubernetes install answers 404. A person only: an API
key gets 403. See [personal-login.md](personal-login.md).

| Method | Path | Role | Description |
|---|---|---|---|
| `GET` | `/me/engine-logins` | viewer | `{"logins": [{"engine", "connected", "pending", "email", "org", "method"}]}` for every engine that offers one |
| `POST` | `/me/engine-logins/{engine}` | developer | Starts the CLI's sign-in → `{"url"}`, the authorization URL to open |
| `POST` | `/me/engine-logins/{engine}/code` | developer | `{"code"}` from the sign-in page → the login's status |
| `POST` | `/me/engine-logins/{engine}/cancel` | developer | Drops a sign-in waiting for its code → `204` |
| `DELETE` | `/me/engine-logins/{engine}` | developer | The CLI's logout, then the volume goes → `204` (`409` while a run has it mounted) |

### Delegated chats

A chat not linked to a task can act on the boards for the person who opened
it. Only that person, signed in (no API key), can turn it on; they or a
workspace admin can turn it off. While it is on:

- The chat's key (`tasks:write`) may change any task in the
  workspace, not only its own. It still cannot decide or delete approvals, apply
  or resolve threads, post to a task's conversation, write to any chat or open
  one, or touch boards, stages, projects, agents, settings or keys.
- Any `DELETE` it sends on task data answers `202` with an `action_id` and
  does not run. The person confirms it in the chat; the request then runs with
  that person's credentials and role (a developer confirming a task deletion,
  which needs admin, gets it refused).
- Only the person it acts for can post messages in it.
- `thulu-github` works in it: the key gets the GitHub token of any project in
  the workspace (`GET /internal/github-token?repo=`), never another
  workspace's (`404`). The token is a GitHub App installation token without
  `contents:write` (it cannot push or merge) and never with
  `deployments:write`; it carries `actions:write` only when the agent's `chat`
  stage grants `run_dispatch`, `run_rerun` or `run_cancel`, and `checks:read`
  only when it grants `pr_checks`. A project that
  reaches GitHub only through a stored credential (a PAT) answers `403`: a PAT
  cannot be narrowed. Each token handed out is audited
  (`chat.github_granted`) with the person the chat acts for.
- Merging a pull request (`pr_merge`) and approving or rejecting a deployment
  (`deployment_approve`) wait for the person, like deletions: thulu-github
  posts them to `/internal/github/…`, the daemon answers `202` with an
  `action_id`, and only the person's confirmation runs them, with a token
  minted for that one call. The agent's `chat` stage has to grant the tool,
  or the request is refused before anyone is asked. `run_dispatch`,
  `run_rerun` and `run_cancel` do not wait.
- Its key carries the person's current role, capped at developer.
- It ends after 8 h, on the switch, when the chat is deleted, or when an
  owner turns the workspace switch off. Ending it revokes the key at once and
  cancels the pending deletions.

---

## Internal endpoints (agent-to-daemon)

These require an API key. They are called by agent containers via `thulu-github` MCP, not by humans.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/internal/projects/{id}/github-token` | Fetch GitHub token for a project |
| `GET` | `/internal/tasks/{id}/github-token` | Fetch GitHub token for the task's primary project |
| `POST` | `/internal/github/{owner}/{repo}/pulls/{number}/merge` | A delegated chat's merge (`merge_method`, `commit_title`, `commit_message`, `sha`). Held for the person's confirmation; runs only as that confirmation |
| `POST` | `/internal/github/{owner}/{repo}/runs/{runId}/deployment-review` | A delegated chat's deployment review (`state`: `approved`\|`rejected`, `environment_ids`, `comment`). Same rules |
| `GET` | `/internal/github-token?repo=owner/name` | Fetch the GitHub token of the caller's project whose repository is `repo` (also takes `https://…`, `.git`, `git@github.com:owner/name`; case-insensitive). How `thulu-github` gets a token in a chat with no task |
| `POST` | `/internal/tasks/{id}/branch-push` | Push task branch to remote (with auto-rebase) |
| `GET` | `/internal/tasks/{id}/branch-status` | Check branch push status + conflict state |
| `POST` | `/internal/tasks/{id}/commit` | Commit staged changes in the task's worktree |
| `GET` | `/internal/environments/{envId}/credential` | Decrypted cloud credential — **only** for environments of the calling task's own projects |
| `GET` | `/internal/credentials/available` | What this session may do: reachable credentials + per-tool verdicts (never a payload) |
| `GET` | `/internal/credentials/resolve` | Authorize one tool call and, on an allow, return the payload. Params: `server`, `tool`, and one of `credential_id` / `environment_id` / `project_id` |
| `POST` | `/internal/token-optimizer/delegate` | One token optimizer delegation, answered by the agent's worker model. Run keys only (see below) |
| `POST` | `/internal/token-optimizer/prepare` | Open a delegation the run's pod makes itself (a `cli` worker): instructions and model. Run keys only |
| `POST` | `/internal/token-optimizer/finish` | Record how a prepared delegation ended. Run keys only |

The session's identity comes from the task-scoped API key, never from a query
parameter: a `?task=` that disagrees with the key is refused outright. Both
credential routes run the same `credentials.Authorize` the MCP's `tools/list`
uses, so nothing can be hidden from the catalog yet callable by name.

### Token optimizer delegation

`POST /internal/token-optimizer/delegate` takes a live run's own key with an
agent; an operator key, or a run key without an agent, is a 403
(`api.token_optimizer.errors.run_keys_only`). The daemon resolves the policy
again for the key's agent and answers 403 `tokenopt.errors.disabled` when the
agent has it `off`, so turning the optimizer off stops runs already going.
The body, at most 1 MiB:

```json
{"kind":"read","question":"…","files":[{"path":"…","content":"…"}]}
```

`read` is the only kind: a `write` is a 400 (`tokenopt.errors.write_removed`),
any other kind `tokenopt.errors.invalid_kind`. The worker gets fixed
instructions and one user message (files as `<file path="…">…</file>`
followed by `Question: …`). One call, no tools, temperature 0.2, 8192 output
tokens, 120 s.

```json
{"id":"dlg_<16 hex>","text":"…","model":"…","provider":"…","input_tokens":0,"output_tokens":0,"cost_usd":0,"duration_ms":0}
```

`cost_usd` is the provider's figure when it reports one (OpenRouter), else an
estimate from the model's price. Errors: 400 malformed body, 413 over 1 MiB,
424 the agent's worker engine has no default profile
(`tokenopt.errors.no_worker`), its default profile cannot back a worker
(`profile_not_usable`, `provider_unsupported`), or it is a `cli` worker
(`tokenopt.errors.worker_in_pod`), 502 the provider failed, 504 it did
not answer in time. Every call past the key check
and the body validation leaves a row in `delegations`, refusals included. The
call goes out through the LLM gateway's guarded transport: a profile's
`base_url` may not reach an internal address unless the operator listed its
host.

A `cli` worker runs in the run's pod. The run asks for the instructions with
`POST /internal/token-optimizer/prepare`, same key and policy check as
`delegate`:

```json
{"kind":"read"}
```

```json
{"id":"dlg_<16 hex>","model":"…","system_prompt":"…","max_output_tokens":8192}
```

The system prompt is the one `delegate` sends. The daemon records a
`delegations` row with status `started`, provider `claude-code`, the profile
and the run's task, chat, agent and stage, and answers with the agent's model.
400 for a `write`; 403 `tokenopt.errors.disabled` when the policy is `off`; 424
when the agent's worker is unavailable (the same reasons as `delegate`) or is
an `api` one (`tokenopt.errors.worker_not_in_pod`). The pod then runs the CLI and reports:

```json
{"id":"dlg_…","status":"ok","error_code":"","files":3,"request_bytes":19000,"response_bytes":900,
 "input_tokens":5200,"output_tokens":300,"cost_usd":0.0026,"duration_ms":11000}
```

`status` is `ok`, `failed` or `timeout`; `error_code` is empty or a
`tokenopt.errors.*` key (`cli_failed`, `timeout`, `upstream_response`,
`empty_answer`). The daemon updates the row only while it is `started` and
belongs to the key's task, chat and agent; anything else, a second finish
included, is a 404. `cost_usd` 0 with tokens is estimated from the model's
price. 200 answers `{"id":"dlg_…"}`.

---

## Git worktree transport

`/git/{taskId}/…` serves the task's worktree over git's smart-HTTP protocol so
remote agent pods can clone/push without host-path mounts. Authenticated by a
task-scoped key sent as the git Basic-auth password (it bypasses the env-key
gate but is NOT unauthenticated). Used by the Kubernetes agent runtime.
