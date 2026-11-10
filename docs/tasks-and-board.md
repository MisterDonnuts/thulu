# Tasks and the Kanban Board

## Boards

A **board** is a workflow: an ordered set of columns with their own rules.
Thulu ships with one, `default`, and you can add more from *Settings →
Boards* (or the `/boards` API) — a short `bugfix` flow beside the full
`feature` one, a `research` flow with no PR stage at all.

A task picks a board when it is created and keeps it for life
(`task.board_id`). The board comes from, in order: the creation request, the
linked project's **default board**, the workspace's default board. Linked
projects that disagree are rejected rather than silently resolved — pass
`board_id`.

**Boards belong to a workspace.** Every board carries a `workspace_id`, and a
caller only ever sees, edits or lands a task on the boards of their own
workspace — a foreign id answers 404, the same as one that does not exist. The
default marker is per workspace too, so `default` is the fallback for whoever
owns it and every other workspace gets a board of its own the first time it
asks for one. `GET /stages` and `GET /task-types` follow the boards: they list
the columns and types of the flows the caller can see, nothing else.

Two consequences worth internalizing:

- **Flags belong to the board, identity is shared.** The same stage id can sit
  on several boards; its label and colour are one concept and are edited
  globally, but `in_progress` can auto-advance on one flow and wait for a human
  on another.
- **Per-stage *config* is shared too** — agent prompts (`prompts/<stage>.md`),
  project hooks (`hooks/<stage>.sh`) and sandbox queue weights are keyed by
  stage id, not by board. That is deliberate: it keeps an agent usable on every
  board that contains the stages it declares as valid (`valid_stages`). If two flows
  need genuinely different behavior for "review", give them different stage
  ids (`qa_review`) — ids are cheap.

Moving a task between boards needs a landing column: if its current stage
doesn't exist on the destination, the API refuses with the destination's
stages to choose from. Deleting a board that holds tasks likewise requires a
destination board; each task keeps its stage where the destination has it and
falls to that board's initial stage otherwise. Neither path dispatches agents.
A workspace's default board cannot be deleted — make another the default first.

## Task types

A **task type** is a named subset of the board: the same columns, walked
differently. Define them in *Settings → Boards → Task types* (or `PUT
/boards/{id}/types`): give the type a label, an icon and a colour, then untick
the columns tasks of that type skip. On a board where you added Refinement and
Review columns, a bugfix can go Backlog → In progress while a feature still
passes both — one board, one screen, one WIP count, an icon on the card telling
them apart.

`task.type_id` carries it; empty means **untyped**, which walks the whole board
and is a perfectly normal state (it is what every task has until you define a
type). New tasks get the board's default type when the request names none.

The rule that keeps this simple: **a type only removes stages, it never changes
flags.** The board stays the single owner of what a column means. So:

- the **initial** stage and every **terminal** stage are part of every type —
  work starts and finishes in the same place regardless of type, which is also
  what keeps "is this task done?" independent of typing;
- if two kinds of work need a column to *behave* differently, that is a
  different board (or a different stage id), not a type.

A column a task's type skips is not a destination for that task: the board still
renders it — other types use it — but the card cannot be dragged there, the
arrow keys step over it, the stage picker omits it, and the API refuses the move
with 409. Auto-advance jumps it and lands on the next column the type keeps.

Editing types never strands work. Removing a type that still holds tasks, or
skipping a column those tasks are sitting in, is refused until you say where
they go; the tasks are then moved administratively (event + webhook, no agent
dispatched). Moving a task to a board that doesn't offer its type simply drops
it to untyped.

## Stages

Stages are **user-customizable**: add, rename, recolor, reorder, and remove
them per board from *Settings → Boards* (or the `/stages` CRUD API with
`?board=`). The board Thulu ships with (`internal/stage/defaults.go`), and
the one every new workspace gets:

| # | ID | Label | Color | Flags |
|---|----|-------|-------|-------|
| 1 | `backlog` | Backlog | `#5d6477` | initial |
| 2 | `in_progress` | In progress | `#f2994a` | pushes branch, wip:work |
| 3 | `done` | Done | `#3aa667` | terminal |

Nothing on it auto-advances, asks for approvals or opens a PR:
a fresh install has no agents, so each operator grows the board into their own
flow.

**Virtual stages** (fixed in code, never board columns): `mention` — an agent
is dispatched out-of-band, read-only, when a user `@mentions` it in a
comment; `chat` — declaring it marks an agent as chat-capable.

### The conversation lane

Mentions and chats run alongside the board, not inside it:

- **The run carries the identity, not the task.** The dispatcher mints a key
  per run that says which agent it launched and on which stage;
  `GET /run/bundle` reads the agent from there. It does not deduce it from the
  `stage_assignments` map, which answers another question (who runs the
  board's stages): a mention on a stage with no agent would fail with 409, and
  one on a stage with an agent would answer with the wrong agent's prompts.
- **It does not wait for the board.** A mention runs while a stage run is
  live; the worktree is mounted read-only in both cases.
- **Each thread is a conversation.** The agent answers as a reply inside the
  thread where it was mentioned, and its run's notices ("was invoked",
  "waiting", failures) land there too. The run gets `THREAD_ID` and the brief
  carries that whole thread. A reply with no `@` goes to the last agent that
  spoke in the thread.
- **One run per (task, thread, agent).** While one works, whatever is said to
  it in that thread is queued and answered together on the next turn. Three
  "@qa" in a row in a thread are one answer, not three containers; the same
  agent mentioned in two threads answers both at once.
- **Agents call each other, with a cap.** An agent that mentions another in
  its reply launches it in the same thread. After 3 agent messages in a row
  since a person's last one, the thread says so and launches nobody else until
  someone writes.
- **The code, always read-only.** A mention or a task chat sees the task's
  worktree or, if it has none yet, the project's checkout on its default
  branch. On Kubernetes it is cloned from the daemon
  (`/git/<task>/<project>`) with the run's or the chat's key.
- **Apply and resolve.** Apply hands the thread to the stage's agent through
  the task's conversation (live run, queue or continuation), so what a thread
  decides gets written with a commit, a push and the auditor. Resolve folds
  the thread down to its root and its last reply, in the UI and in the brief;
  a reply from a person reopens it. Neither works with a run's key.
- **A chat agent (declares `chat` and not `mention`) answers from one chat per
  thread.** It opens the first time the agent is mentioned in that thread and
  shows in the sidebar; each turn gets what was said in the thread since its
  last reply. If someone is writing to that chat, the mention waits its turn
  instead of asking you to repeat it.
- **Its own quota:** `mention.max_containers` (default 3), separate from
  `agent.max_containers`. A full board does not leave agents unreachable, and
  a burst of questions does not eat the stages' slots.
- **It fails out loud.** If the agent has no prompt, the mention is refused
  before spending a container; if the run dies while downloading its bundle,
  the comment says why instead of "finished (exit N) but left no reply".

### Stage capability flags

Every flow behavior is a flag; no stage id is special. No flag says whether a
column runs an agent: every column between the initial one and the terminal
ones runs the agent the task has in `stage_assignments[stage]`, and with none
there the task waits in the column.

- **initial** (exactly one): new tasks are created here; hard dependencies
  gate leaving it; the worktree is branched when a task leaves it.
- **terminal** (at least one): entering finishes the task — sandbox released,
  worktrees removed, dependents unblocked; excluded from open counters.
- **auto_advance**: a clean agent exit (code 0) moves the task to the next
  stage in board order.
- **required_approvals** (`[{key, label, task_types, reset_on_change}]`):
  entering the stage opens one pending approval per entry that applies to the
  task's type; forward moves out of the stage wait until every one is approved
  or waived. A stage that auto-advances leaves the task in
  `awaiting_approval` and the last decision moves it. `reset_on_change`
  (default true) sends a given approval back to pending when a later run in the
  stage leaves new commits.
- **on_changes_requested** (`resume` default | `kick_back` | `stay`) and
  **changes_kick_back_to**: what "request changes" on one of the stage's
  approvals does.
- **push_branch**: a clean run pushes the task branch to the remote.
- **open_pr**: entering this stage opens the pull request (root tasks only).
- **wip_group** (`work` | `review` | empty): which dashboard counter the
  stage's tasks feed.
- **ingest_flags** and **blocked_by_flags**: with `ingest_flags`, a
  `flags.yaml` in the run output becomes agent approvals with no stage, and
  those only hold a task on stages with `blocked_by_flags`. Neither is set on
  the factory board.

### Rules

- Stage ids are immutable slugs (`[a-z0-9_]{1,32}`, no hyphens); renaming is
  a label change. `mention`, `chat`, `archived`, `queued` are reserved. Board
  ids follow the same rule.
- The invariants hold **per board**: exactly one initial, at least one
  terminal, no stage both, terminal stages neither auto-advance nor need an
  agent.
- The initial stage and the last terminal stage cannot be removed from a
  board. Removing one that still holds tasks requires a `move_to` destination;
  the tasks are relocated without dispatching agents.
- A stage exists as long as at least one board uses it. Removed from its last
  board, it leaves the catalog.
- Per-stage config referencing a deleted stage is kept and surfaced as
  *orphaned* — `thulu.*` labels and `prompts/<stage>.md` / `auditor/<stage>.md`
  in the agent settings UI, `prompts`/`hooks` files in the project's stage-file
  modal. It becomes active again if a stage with that id is recreated, and each
  view has a **Clean up** button that deletes it for good. A stage still used by
  another board is not an orphan.

---

## Task object

```typescript
interface Task {
  id: string;
  project_ids: string[];          // linked project IDs (can span multiple repos)
  title: string;
  board_id: string;
  type_id: string;                // "" = untyped
  parent_id?: string;             // set on subtasks
  stage: Stage;
  stage_assignments: Record<Stage, string>;  // stage → agent_type
  branch_name: string;            // e.g. "task-abc1234"
  worktree_paths: Record<string, string>;    // project_id → host path
  pr_urls: Record<string, string>;           // project_id → PR URL
  current_run_id: string;
  current_instance: string;
  status: RunStatus;
  tags: string[];
  priority: string;
  created_at: string;
  updated_at: string;
  // Derived (populated by Get, not List):
  body: string;                   // markdown; "" on every task from a list
  comments: Comment[];
  attachments: Attachment[];
  approvals: Approval[];
  pending_approvals: number;      // not yet approved or waived
  questions: Question[];
  test_cases: TestCase[];
  children: Task[];               // subtasks, one level deep
  is_blocked: boolean;
  blocked_by_count: number;
  blocked_by: DepRef[];
  blocking: DepRef[];
}
```

### Paginating a list

`GET /tasks` returns every matching task when it is called with no pagination
params, which is what the board does. Pass `?limit=` (capped at 500) to get a
page instead, and the response carries an `X-Next-Cursor` header while more
rows remain; feed that token back as `?cursor=` for the next page. When the
header is missing the last page was reached — a page can come back short or
even empty and still have a successor, because the workspace and subtask
filters run after the rows are read, so page until the header stops appearing.

The cursor is opaque and keyset-based, not an offset: it names the `(updated_at,
id)` pair of the row the page ended on. Tasks are reordered by every stage move,
so an offset would skip rows that slid past the boundary and repeat rows that
slid the other way. The same params work on `GET /usage/sessions` and
`GET /audit`.

### RunStatus (orthogonal to Stage)

| Value | Meaning |
|-------|---------|
| `idle` | No run in flight; last run finished cleanly (or never run) |
| `running` | Agent container is currently alive |
| `out_of_turns` | Last run hit `error_max_turns` |
| `failed` | Last run exited non-zero for other reasons |
| `blocked` | Auditor verdict: `block` — needs human intervention |
| `needs_more_turns` | Auditor verdict: `needs_more_turns` |
| `awaiting_input` | The run ended with a blocking question open; answering it resumes the same session |
| `awaiting_approval` | The run ended cleanly on an auto-advance stage whose approvals are not settled |
| `queued` | The run was accepted but the workspace has no room for its pod yet (Kubernetes: the namespace quota is held by the workspace's other pods). It starts on its own when one of them ends, or gives up after `agent.max_queue_minutes` and the task turns `failed` |

`status` is reset to `idle` on any user-driven stage transition, which also
cancels a `queued` run.

---

## Stage transitions

### User-driven

```
PATCH /tasks/{id}  { "stage": "in_progress" }
```

The dispatcher receives `OnStageChange` and launches the agent assigned to the target stage. With no agent assigned, the move still happens and no run starts. The task gets a system comment only when the board runs an agent in that column elsewhere (a linked project's stage template, or another task of the board), so a column people work by hand stays quiet. Forward moves out of a stage with unsettled approvals are refused.

### Auto-advance

When an agent container exits cleanly and the current stage has `AutoAdvance: true`, the dispatcher moves the task to the next stage its type keeps and immediately attempts to launch that stage's agent. If the stage has unsettled approvals, the task waits in `awaiting_approval` instead.

### Relaunch

```
POST /tasks/{id}/relaunch
```

Re-runs the currently assigned agent on the current stage. Useful after a `failed` or `out_of_turns` status.

---

## Stage assignments

`stage_assignments` maps each stage to an agent type name. Populated at task creation from the union of linked projects' `stage_template` fields, then editable at any time via `PATCH /tasks/{id}`.

```json
{
  "stage_assignments": {
    "in_progress": "implementer"
  }
}
```

The values are names of agent types in the task's workspace. Stages not in the map (e.g.
`backlog`, `done`) have no agent and wait for a person to move the task.

---

## Multi-project tasks

A task can span multiple projects (e.g. a change that touches a frontend and a backend repo). The dispatcher creates one worktree per project on the task branch `task-<id>`. Agents get all worktrees mounted under `/workspace/projects/<sanitized-name>/`.

Entering an `open_pr` stage opens one PR per project. The daemon does not open a PR at the end of a run: besides `open_pr`, a PR opens with Force PR or when the agent calls thulu-github `pr_create`, which links it to the task.

---

## Task dependencies

Dependencies model "this task is blocked by that task". Two kinds:

| Kind | Behavior |
|------|----------|
| `hard` | The task cannot leave its board's initial stage until every hard blocker reaches a terminal stage of its own board. |
| `soft` | Advisory only — visible in UI, does not gate transitions. |

```
POST /tasks/{id}/dependencies  { "depends_on_task_id": "abc123", "kind": "hard" }
DELETE /tasks/{id}/dependencies/{depId}
GET /tasks/{id}/dependencies
```

The `is_blocked` and `blocked_by_count` fields on every task in `GET /tasks` are derived from a single aggregate query over `task_dependencies` — they are never stored on the task row.

---

## Comments

Comments are anchored to the stage they were made in. Sources:

| Source | Who |
|--------|-----|
| `user` | Human via dashboard |
| `agent` | The stage agent |
| `auditor` | Post-run auditor |
| `system` | Daemon (lifecycle events) |
| `automation` | A rule or integration acting without a person |

```
POST /tasks/{id}/comments  { "body": "LGTM" }
```

---

## Reports

Long-form markdown artefacts (agent index, auditor feedback). Anchored to stage + instance. Separate from the comment timeline — shown in their own "Reports" tab in the task drawer.

---

## Approvals

Approvals are the sign-offs a task needs. A stage opens them from its
`required_approvals`; an agent can add one with `task_request_approval`; the
daemon adds system ones (an observability proposal tied to the task, for
instance); a person can add one by hand. Statuses: `pending`, `approved`,
`changes_requested`, `waived`. Only a person decides, waives or deletes one;
every run key is refused.

```
GET    /tasks/{id}/approvals
POST   /tasks/{id}/approvals                 { "key": "design", "label": "Design sign-off", "reason": "..." }
POST   /tasks/{id}/approvals/{aid}/decide    { "decision": "approved", "note": "..." }
DELETE /tasks/{id}/approvals/{aid}
```

The `/tasks/{id}/flags` routes are aliases over the same rows.

---

## Workflow events

Immutable event log per task (not the same as the audit log). Kinds include:

- `task_created`, `stage_entered`, `agent_run_started`, `agent_run_finished`
- `auditor_run_started`, `auditor_verdict`, `mention_started`, `mention_finished`
- `approval_requested`, `approval_decided`, `approval_reset`, `approval_waived`
- `question_asked`, `question_answered`, `run_awaiting_input`
- `branch_created`, `pr_opened`, `dep_added`, `dep_resolved`, `test_case_run`

```
GET /tasks/{id}/events
```

---

## Clone vs Fork

| Operation | What it does |
|-----------|--------------|
| `POST /tasks/{id}/clone` | New task at same stage, fresh worktrees, empty sub-collections |
| `POST /tasks/{id}/fork` | Deep copy: copies all comments, approvals, events, attachments (files physically copied on disk) |

Fork reads the source rows in one snapshot before performing the inserts so the new task isn't built from a half-mutated parent.

---

## Attachments

Agents and users can upload files. Stored on disk under `data/attachments/<task_id>/`. Served at:

```
GET /task-attachments/{attachmentId}
```

Filenames are sanitized: only `[a-zA-Z0-9._-]` are kept, capped at 120 characters.
