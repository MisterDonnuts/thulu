# Structured task sections

JIRA-like sections (Goal, Definition of Done, Test Cases, …) on top of the
task body, without a parallel store: **the markdown body stays the single
source of truth**. Sections are `## Heading` blocks; checklists are GFM
`- [ ]` / `- [x]` lines. Everything that already consumes the body (the agent
brief, `task_get`, the web renderer) keeps working unchanged.

## Why not a table

A `task_sections` child table would be a second source of truth against the
body: every consumer (brief assembly, web render, exports) would need dual
paths, and drift between the two would be invisible until it bit an agent.
Parsing the body on demand costs microseconds and can't drift.

The robustness instead comes from *how it's written*: *agents and the UI
never string-edit the body*. They address one section or one checklist item
and the server does the read-modify-write, serialized per daemon
(`Server.sectionsMu`), because `Store.Update` is last-write-wins full-row.

Test cases are the exception. Each case carries a status, a history of runs
and the files every run produced, and QA filters them across all the tasks of
a workspace, none of which a checklist line can hold. They live in their own
tables (`test_cases`, `test_case_runs`), described in
[test-cases.md](test-cases.md). The `## Test Cases` section parses and
renders, and "Import from body" turns it into rows; once a task has rows, the
rows are what the brief lists.

## Canonical sections

In canonical order: `goal`, `context`, `definition-of-done`, `test-cases`,
`out-of-scope`, `risks`, `notes`. Custom sections are allowed (explicit title,
appended at the end). Slugs are derived from headings by
`task.SlugifySection` (Go) / `slugifySection` (web) — keep them in sync.

Parsing rules (`internal/task/sections.go`):

- Only `## ` (level-2) headings open a section; `###`+ belongs to the
  enclosing section. Text before the first `## ` is the **preamble** and is
  always preserved — a human's original description survives refinement.
- Headings and checklist lines inside fenced code blocks are ignored.
- Checklist indexes are 1-based and count checklist lines within one section.

## API

| Route | Verb | Role | Does |
|---|---|---|---|
| `/tasks/{id}/sections` | GET | viewer | parsed view (preamble + sections + items) |
| `/tasks/{id}/sections/{slug}` | PUT | developer | create/replace one section (`{title?, content}`) |
| `/tasks/{id}/sections/{slug}/items/{index}` | PATCH | developer | check/uncheck one item (`{checked, note?}`) |

A non-empty `note` is appended to the item line after an em-dash, so evidence
travels in the task itself: `- [x] PDF has all columns — verified in TestExport`.
Both writes record a workflow event (`section_updated` / `checklist_toggled`).
The author comes from the credential that made the request; the body cannot
name one.

## MCP (octopus-api)

`task_sections_get` (read), `task_section_set` (write),
`task_checklist_check` (write). Two system skills that travel with every run
wired to octopus-api teach agents how to use them:

- `octopus-refinement`: write goal, context, Definition of Done, out-of-scope
  and risks as sections of the body, and test cases as rows.
- `octopus-task-workflow`: tick a Definition of Done item with
  `task_checklist_check` and a `note` that carries the evidence.

## Web

The task drawer renders `- [ ]` as real checkboxes (interactive inside `##`
sections of the body; read-only in comments/reports). A click PATCHes the
item endpoint — never a raw body PATCH — then refetches the task to resync
the editor.
