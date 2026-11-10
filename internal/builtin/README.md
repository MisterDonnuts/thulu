# builtin

What ships inside the daemon binary (`embed.go`).

## system-skills/

System skills: how to use the internal tools (`thulu-browser`, `octopus-api`,
…). Each one has the format in `docs/skills-authoring.md` (`skill.yaml` with
the metadata, `prompt.md` with the content) and two fields of its own in
`skill.yaml`:

- `version`: an integer that only goes up. At startup, the daemon copies a
  skill into the database when the embedded version is higher than the stored
  one (`agentstore.SyncSystemSkills`). If the stored one is equal or higher it
  leaves it alone, so an operator's hotfix survives a restart of the same
  release.
- `mcp`: the server it travels with. Every run with that MCP wired on its
  stage gets the skill without the agent granting it. Without `mcp`, every run
  gets it.

Names start with `octopus-`: that prefix is reserved and no workspace can
create a skill with it. Workspaces read them in `/skills/` but do not edit
them.

A workspace admin can turn a system skill off for an agent on a stage: the
agent's Skills tab in `/agents/`, or `system_skills_off` in
`PUT /types/{name}/settings`. `octopus-run` can be turned off too, and the UI
warns. `types.AgentType.StageSkills` decides it, and it is what stage,
mention and resumed runs, the chat and the run bundle read. The opt-outs are
stored in the agent's row, inside the `skills_by_stage` column with keys
`!system_off:<stage>` (no migration); in compose mode, in the
`thulu.skills.system_off.<stage>` labels. A name that is no longer a system
skill is ignored.

For an urgent fix without cutting a release, the operator (an admin of the
`OCTOPUS_OPERATOR_WORKSPACE` workspace) uses the API:

```sh
curl -X PUT "$OCTOPUS/system/skills/octopus-test-cases" \
  -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{"files": {"prompt.md": "…", "skill.yaml": "…"}}'
```

For a single file there is `PUT /system/skills/{name}/files/{path}` with the
text in the body. The hotfix keeps the stored version: restarts of the same
release do not overwrite it and the first release that bumps `version`
replaces it. So the fix also has to land in this directory, with the version
bumped, before the next release.

| Skill | MCP | What it teaches |
|---|---|---|
| `octopus-sandbox` | `thulu-sandbox` | what runs in the sandbox and what in your container, starting a project, writing the init script with tools/setup/cache, what survives, limits, failures by symptom |
| `octopus-browser-evidence` | `thulu-browser` | filmed takes, annotated captures, before/after, report |
| `octopus-evidence-library` | `thulu-browser` | recording an Evidence library case in a recording chat: explore, film, verify, test logins with `secret`, save once with `evidence_save_case` |
| `octopus-test-cases` | `octopus-api` | a task's test cases, runs with their evidence, statuses, the QA view |
| `octopus-run` | none (`always`) | run types, repos and output dir, who commits and opens the PR, what gets published, what happens at the end |
| `octopus-task-workflow` | `octopus-api` | questions and blockers, approvals and requested changes, comments and mentions, body sections, subtasks and dependencies |
| `octopus-refinement` | `octopus-api` | getting a task ready to work on: goal, Definition of Done, cases, routing, how to split it |
| `octopus-observability` | `octopus-api` | mappings of the fixed signals, dashboards of custom metrics, what to report when telemetry is missing |
| `octopus-token-optimizer` | none (`when: token_optimizer`) | delegating large reads to a cheaper model with `octopus-delegate`, and what to check before editing |

The examples that come with a skill are validated in its tool's tests
(`tools/thulu-browser/test/skill-examples.test.js`), so a skill cannot teach a
format the tool rejects.

## starter/pack.json

The starter pack, in `agentstore` format (the same as
`thulu agents export`). Each workspace gets it copied the first time its
catalog is used (`agentstore.EnsureSeeded`); from then on what was copied
belongs to the workspace, which edits it as it likes. Each copied row keeps its
origin and the pack version. A new pack version does not apply itself to
workspaces already seeded.

It is empty: a new workspace starts with no agents and creates them from
`/agents/` or with `thulu agents import -workspace <id> <tree>`.

## workshop/

The Workshop catalog: agents and skills an admin installs in their workspace
from `/agents/` or `/skills/`. It is not copied into the database at startup;
each workspace installs what it picks, and what it installs becomes its own.

```
workshop/
├── agents/<name>/agent.json      ← {"revision": 1, "tags": [...], "definition": {...}}
├── agents/<name>/prompts/main.md ← the agent's files, as the workspace stores them
├── skills/<name>/skill.yaml      ← with version (the revision) and tags
└── skills/<name>/prompt.md
```

- `definition` is the same JSON `GET /types/{name}` returns, without `dir`.
  The agent's skills go per stage in `skills_by_stage`; installing the agent
  also installs the catalog skills it names and the ones those ask for in
  `requires`.
- Changing an item's content requires bumping its revision (`revision` in
  `agent.json`, `version` in `skill.yaml`). That way a workspace that installed
  it and did not touch it sees "Update available". One that edited it sees a
  conflict and decides.
- Do not use the `octopus-` prefix: it belongs to the system skills.
- `go test ./internal/workshop/` loads the whole catalog; a broken item fails
  the test.
