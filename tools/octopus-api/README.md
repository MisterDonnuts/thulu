# octopus-api: MCP wrapper over Thulu's REST API

MCP service that exposes the daemon's API as tools. Any agent whose stage wires
it uses it: to ask questions, request approvals, comment, write sections and
test cases, split work into subtasks, and also to create and modify tasks,
agents and projects from a chat.

It has no `skill.md`: what the agent needs to know to use it is taught by the
system skills with `mcp: octopus-api` (`octopus-task-workflow`,
`octopus-refinement`, `octopus-observability`, `octopus-test-cases`), which
travel on their own to every run that has it wired.

## Scopes

- `read`: list/read tasks, agents, projects, catalog
- `write` ⊃ read: create/patch/delete tasks, agents and projects, write agent
  type prompts and settings

## Boards: which one an agent writes to

An agent **does not choose** its board. The daemon adds `?task=<id>` to the SSE
URL (`images/snippets/mcp-config.sh`), this server resolves that task's board,
and the everyday tools are pinned to it:

| Tool | Reach |
|---|---|
| `board_current` | where am I: the board, its columns in order, and which one the task is in |
| `task_create` | creates **on my board**. It does not accept `board_id` |
| `task_list` | lists **my board** |

Crossing boards takes **other tools**, and they sit **outside the scope
ladder**: `@read` does not give them and neither does `@write`. The only way to
grant them is to **name them**.

| Tool | Reach |
|---|---|
| `boards_list` | read the catalog of boards |
| `board_tasks_list` | read the tasks of another board |
| `task_create_on_board` | create on an explicit board |
| `task_move_to_board` | move a task to another board (board + column together) |

```json
"mcp_by_stage": {
  "in_progress": ["octopus-api@write"],
  "planning":    ["octopus-api@task_create+task_list+boards_list+task_create_on_board"]
}
```

`in_progress` gets all of its own board and no access outside it, which is what
`@write` does. `planning` is an agent that legitimately crosses flows: that has
to be written out.

The list is separated by `+`, not by a comma: `MCP_TOOLS` and the
`thulu.mcp.default.<stage>` import label are CSV, so a comma inside an entry
would be split into phantom servers on the next read. With no suffix,
`octopus-api` defaults to `read`.

The default is closed on purpose: a permission you have to *remember to remove*
ends up granted by accident, and writing to the wrong board raises no error, it
files the work where nobody looks.

**Listing is not granting.** The UI picker probes with `?catalog=1`
(`internal/mcp/probe.go`): with that param `tools/list` returns the whole
surface so the cross-board tools can be **seen and ticked**, but `CallTool`
still answers to the real permission. Without it, the capability would be
invisible on the very screen where it is granted, which makes it impossible to
limit in practice.

Two things happen here and not in the daemon, on purpose:

- **The column is validated before creating or moving.** A stage id can exist
  on several boards or on none; a task sitting in a column its board does not
  have is real and invisible, and from then on every flow question is answered
  by a fallback board.
- **`task_update` cannot change the board.** The API accepts it, so without
  this block the handler's `...patch` would let an undeclared `board_id`
  through, a bypass of the per-tool permission. Moving is what
  `task_move_to_board` is for.

## Which tasks the run's key can touch

This server passes the run's key as is, and the daemon decides. With the key of
a run on task A, the tools that take a task id (`task_update`,
`task_add_comment`, `task_relaunch`, `task_move_to_board`, `task_section_set`,
`task_attach`, `task_clone`, `task_add_dependency`, …) write only to A, its
parent, its sibling or child subtasks, and the tasks A created (the daemon marks
them with `origin_task_id`). On any other task of the workspace they return
`403`. Reading stays open to the whole workspace. No run can decide, skip or
delete an approval. The full rule is in `docs/security-hardening.md`
(section 6).

A chat with no task has no `tasks:write`: it reads the board but does not move
tasks with its key, unless the person delegates permission to it ("Act for me",
`POST /chats/{id}/delegation`).

## Questions and approvals

`task_ask` and `task_request_approval` act on the session's task (`?task=`),
never on an id the model passes. The daemon accepts a question only from the
run that asks it (`POST /tasks/{id}/questions`), and an approval is a gate on
the task this run is moving (`POST /tasks/{id}/approvals`). Without `?task=`,
both fail before calling the daemon.

`task_ask` checks the limits before sending: 1 to 4 questions, a header of at
most 12 characters (characters, not bytes), 0 to 6 options with unique labels
(the answer names the option by its label) and a required `default` if
`blocking` is false. The body goes out with explicit defaults (`blocking: true`,
`multi_select: false`, `allow_other: true`). The tool first returns the
daemon's `instruction` as plain text; on a blocking question it tells the agent
to end its turn, and that is what parks the run.

Answering, approving, skipping or deleting have no tool: those routes are for
people only.

## Tests

```bash
cd tools/octopus-api && npm install && node --test
```

`server.test.mjs` starts the server against a fake daemon and checks the boards
contract end to end (pinning, column validation, and that the per-tool
allowlist really hides **and** blocks the cross-board tools).

## Port

`:3013` (host) → `:3000` (container). Configured in `tools/docker-compose.yml`.

## Wiring from an agent

The daemon forwards `MCP_OCTOPUS_API_URL` (from its env or from `/mcps`) and the
agent type grants it per stage in `mcp_by_stage` (MCP tab in `/agents/`), as in
the example above. On the agents network the URL is
`http://octopus-api:3000/sse`.

## Health

```bash
curl http://localhost:3013/health
```

## Tools

Tasks (my board): list, get, create, update, delete, clone, relaunch,
add_comment, add_report, subtask_create.
Sections: task_sections_get, task_section_set, task_checklist_check.
Attachments: task_attach, task_attachments, task_attachment_delete.
Dependencies: task_list_dependencies, task_add_dependency,
task_remove_dependency.
Questions and approvals: task_ask, task_request_approval.
Test cases: task_test_cases, task_test_case_add, task_test_case_update,
task_test_case_delete, task_test_case_run. `case` accepts the key (`TC-3`) or
the id. A run with a verdict leaves the case in that status and covered. The
verifies of `evidence_publish` (thulu-browser) whose `case` is a key of the
task are recorded as runs on their own: no need to repeat them with
`task_test_case_run`. Test cases live in their own rows, not in a `## Test Cases` section
(`docs/test-cases.md`).
Boards: board_current, boards_list, board_tasks_list, task_create_on_board,
task_move_to_board.
Agent instances: list, get, stop.
Agent types: list, get_settings, list_prompts, list_files, read_file,
update_settings, write_file, create, delete.
Projects: list, get, create, update, delete.
Catalog: skills_list, tools_list, stages_list.
Environments: environment_list, environment_mapping_list,
environment_mapping_propose.
Custom metrics: custom_metric_list, custom_metric_series.
Dashboards: chart_types_list, dashboard_list, dashboard_get, dashboard_data,
dashboard_propose.

Proposing, yes; approving, no: a person enables a mapping, a custom metric or a
dashboard, and there is no tool for that in any of the three cases.
