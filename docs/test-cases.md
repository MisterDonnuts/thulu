# Test cases

A task's test cases are rows of their own: a key (`TC-1`), a title, a
description, steps, the expected result, a status, and every run that
exercised the case, each with the video, the replayable take (JSON) and the
report it produced. QA reads them in the task drawer and across tasks
in the "From tasks" tab of Evidence (`/evidence/`).

## Why a table, when sections live in the body

`docs/task-sections.md` keeps Goal, Definition of Done and the rest in the
markdown body, and that still holds for them. Test cases live outside the body because
they carry what markdown cannot hold without breaking: a status per case, a
history of runs, attachments bound to each run, and a query across every task
of a workspace ("everything failing on this board"). A body section the UI and
agents rewrite on every run would drift under a human edit and cannot be
filtered.

The `## Test Cases` checklist in the body parses and renders.
A task with that section and no rows offers "Import from body", which turns
each `- [ ]` line into a case (`- [x]` lines come in as `pass`, covered). A
line that starts with a key (`- [ ] TC-3: login works`) keeps that key and
drops it from the title; lines whose key or title the task already has are
skipped. Once
a task has rows, the rows are the source of truth and the agent brief lists
them instead of relying on the section.

## Model

`test_cases`

| column | type | notes |
|---|---|---|
| `id` | text pk | generated like every other id |
| `task_id` | text | FK `tasks(id) ON DELETE CASCADE` |
| `key` | text | `TC-<n>`, unique per task; assigned as max+1 when not given; a given key must match `^[A-Za-z][A-Za-z0-9]*-[0-9]+[a-z]?$` |
| `title` | text | required, ≤ 200 chars |
| `description` | text | markdown: context, preconditions, data |
| `steps` | text | markdown, usually a numbered list |
| `expected` | text | markdown: the expected result |
| `priority` | text | `low`, `medium` (default), `high`, `critical` |
| `status` | text | `pending` (default), `pass`, `fail`, `blocked`, `skipped` |
| `covered` | bool | someone exercised it (a run exists, or QA ticked it by hand) |
| `position` | int | order within the task; new cases go last |
| `created_by`, `updated_by` | text | author, derived from the credential like comments |
| `created_at`, `updated_at` | timestamptz | |

`UNIQUE (task_id, key)`, index on `(task_id, position)`.

`test_case_runs`

| column | type | notes |
|---|---|---|
| `id` | text pk | |
| `test_case_id` | text | FK `test_cases(id) ON DELETE CASCADE` |
| `task_id` | text | FK `tasks(id) ON DELETE CASCADE`; kept for listing and deletion |
| `verdict` | text | `pass`, `fail`, `blocked` |
| `observed` | text | what was seen, short |
| `note` | text | markdown |
| `video_attachment_id` | text | `''` or an attachment of the same task |
| `take_attachment_id` | text | `''` or an attachment of the same task (the take JSON) |
| `comment_id` | text | `''` or a comment of the same task |
| `report_id` | text | `''` or a report of the same task: the evidence report |
| `capture_attachment_ids` | jsonb | array of attachment ids of the same task |
| `author` | text | |
| `ran_at` | timestamptz | |

Index on `(test_case_id, ran_at DESC)`.

Recording a run sets the case's `status` to the run's verdict and `covered` to
true, in the same transaction. Every attachment, comment and report id a run
names is checked to belong to the run's task (404 otherwise), so a run cannot point at
another tenant's file.

Deleting a task deletes its cases and runs. Deleting an attachment a run points
at leaves the run with a dangling id that the UI shows as "file deleted".

## API

`{case}` accepts the case id or its key (`TC-3`).

| Route | Verb | Role | Does |
|---|---|---|---|
| `/tasks/{id}/test-cases` | GET | viewer | the cases in order, each with its `runs` newest first |
| `/tasks/{id}/test-cases` | POST | developer | add one case, or several with `{cases: [...]}`; returns the created cases |
| `/tasks/{id}/test-cases/import-body` | POST | developer | turn the body's `## Test Cases` checklist into cases; a leading `TC-n:` sets the key; skips keys and titles already present |
| `/tasks/{id}/test-cases/{case}` | PATCH | developer | partial update: `title`, `description`, `steps`, `expected`, `priority`, `status`, `covered`, `position` |
| `/tasks/{id}/test-cases/{case}` | DELETE | developer | removes the case and its runs |
| `/tasks/{id}/test-cases/{case}/runs` | POST | developer | record a run (`verdict` required) |
| `/test-cases` | GET | viewer | QA view across the caller's workspace |

`GET /test-cases` query: `status` (comma list), `board_id`, `project_id`,
`q` (matches key, title, task title), `uncovered=1`, `limit` (default 200,
max 1000). Each row is the case with `task_title`, `task_stage`, `board_id`,
`last_run` (or null) and `runs_count`, ordered by task updated_at desc, then
position. The response is `{ "test_cases": [rows], "counts": {status: n},
"covered_counts": {status: n} }`; `covered_counts` counts the covered cases of
the same set.
`counts` has every status, counted over the whole filtered set (not only the
page) with the `status` filter left out, so the QA view's status chips keep
their totals while one of them is picked.

Answers of the task routes: GET returns a bare array of cases; POST returns
the created cases as an array (201), even for one; `import-body` returns the
created array (200, `[]` when nothing is new); PATCH returns the case; DELETE
returns `{"status":"deleted"}`; a run returns `{ "run": …, "test_case": … }`
(201) so the caller sees the case's new status. The author and `ran_at` come
from the credential and the clock, never from the body.

JSON shapes:

```json
{ "id": "…", "task_id": "…", "key": "TC-1", "title": "…", "description": "…",
  "steps": "…", "expected": "…", "priority": "medium", "status": "pass",
  "covered": true, "position": 1, "created_by": "…", "updated_by": "…",
  "created_at": "…", "updated_at": "…",
  "runs": [ { "id": "…", "test_case_id": "…", "task_id": "…", "verdict": "pass",
              "observed": "…", "note": "…", "video_attachment_id": "…",
              "take_attachment_id": "…", "comment_id": "", "report_id": "…",
              "capture_attachment_ids": ["…"], "author": "…", "ran_at": "…" } ] }
```

`GET /tasks/{id}` includes `test_cases` (with runs) next to `comments` and
`attachments`, so the drawer needs no extra request.

Every write records a task timeline event (`test_case_added`,
`test_case_updated`, `test_case_deleted`, `test_case_run`) and notifies the
`tasks` live topic so an open drawer refreshes without waiting for the poller.
A run-scoped key may write the test cases of its own task, as with comments.

Export/import carries cases and runs with their ids. Clone copies the cases
(status back to `pending`, not covered, no runs). Fork copies the cases the
same way.

## Agents

MCP tools in `octopus-api`:

| Tool | Scope | Does |
|---|---|---|
| `task_test_cases` | read | list the task's cases with their runs |
| `task_test_case_add` | write | add one or several cases |
| `task_test_case_update` | write | edit fields, status or covered |
| `task_test_case_delete` | write | remove a case |
| `task_test_case_run` | write | record a run with its evidence ids |
| `task_add_report` | write | file a markdown report in the Reports tab; its id is the run's `report_id` |

The task brief (`GET /tasks/{id}/brief`) lists the cases with key, title,
expected and status, so every stage sees them. The `octopus-test-cases` system
skill explains these tools to the agent; every run whose stage wires
`octopus-api` receives it.

`thulu-browser`'s `evidence_publish` posts its report to the task's Reports tab
(`POST /tasks/{id}/reports`, kind `evidence`), so the comment thread keeps only
the conversation. It also records runs by itself: every verdict whose `case`
equals a case key of the task becomes a run of that case, with the video, the
take, the captures and the `report_id` of that report. Several verdicts with
the same `case` make one run: `fail` if any failed, `blocked` if a failed step
of the replay kept one of its checks from running, `pass` only when every check
ran and passed. In a before/after, the
"after" side is the one recorded. Keys the task does not have are listed in
the tool's answer and not recorded.

## Web

The task drawer shows a "Test cases" block after the body: a progress bar by
status, covered N/M, and one expandable row per case (key, title, status,
covered and passing checks). Expanded, a case shows its description, steps,
expected result, and its runs with the video inline and links to the take and
the captures. A run with a `report_id` carries its report folded underneath,
rendered as markdown, and an "Open in Reports" link that switches to the
Reports tab with that report unfolded. A run with a `comment_id` links to the
comment instead. Cases can be added, edited, reordered, deleted and marked
from there.

`/tasks/?board=<board>#task-<id>/<key>` opens the drawer on that case (the
board is optional; the QA view always sends it so the board behind the drawer
is the task's).

The "From tasks" tab of Evidence (`/evidence/`) lists cases across tasks with filters by
status, board, project, text and "uncovered only", status totals on top, and a
link from each row to the case in its task.
