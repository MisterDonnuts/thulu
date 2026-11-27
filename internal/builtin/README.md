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
├── skills/<name>/prompt.md
└── flows/<name>/flow.json        ← a board: columns, agents per column and per type, task types
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
  the test. `go test ./internal/builtin/` checks that its Markdown is in
  English (`descriptions.es` is the only Spanish).

| Item | Kind | Stage | What it does |
|---|---|---|---|
| `sample-developer` | agent | `in_progress` | example coding agent to copy from |
| `changelog-writer` | agent | `in_progress` | writes the CHANGELOG entry of the branch, with `changelog-style` |
| `thulu-refiner` | agent | `refinement` | Thulu flow: explores, reproduces, asks at most 5 questions, writes the spec and `flags.yaml` (surface, risk, size and the later columns the task skips, each with a reason); writes no files and no TC-n (`octopus-test-cases` is off) |
| `thulu-test-designer` | agent | `test_design` | Thulu flow: classifies feature/bug/refactor, writes tests red by assertion (or characterization in green), stubs marked `thulu:stub`, TC-n, user tasks and `locked-tests.txt` |
| `thulu-developer` | agent | `in_progress` | Thulu flow: implements against the locked tests and ends with `verdict.md` (`block` with `spec_test_conflict` or `nothing_to_change` when it must stop); its stage needs `agent_verdict` |
| `thulu-verifier` | agent | `verification` | Thulu flow: runs `thulu-gates` on the new code, triages the report and writes `verdict.md`; critical mutants go back to `test_design`, the rest to `in_progress` |
| `thulu-reviewer` | agent | `ai_review` | Thulu flow: reviews against the spec, refutes every candidate finding with a subagent and publishes a `COMMENT` review on the draft PR |
| `thulu-agentic-tester` | agent | `testing` | Thulu flow: tests like a manual QA through `thulu-browser` (functional, visual, blind user, timings, accessibility) and asks for `ux_review` when the usability check fails |
| `thulu-readiness` | agent | `readiness` | Thulu flow: read-only last check before human review; branch up to date and mergeable, commit rules, no debug, evidence or secret files, spec and answers met, test cases run on the current head with evidence, earlier reports consistent; sends back to `in_progress`, `verification`, `ai_review` or `testing`, or blocks |
| `thulu-release` | agent | `release` | Thulu flow: confirms what reached production, runs production-safe smoke and watches health; in batch mode deploys only after the `deploy_prod` approval |
| `changelog-style` | skill | | Keep a Changelog entries people read |
| `spec-writing` | skill | | exploration budget, ambiguity sweep, questions, sections and their slugs, surface/size/risk, splitting, Ready check |
| `test-design` | skill | | red for the right reason, oracles from the spec, mocks at boundaries, properties, characterization, flakiness, TC-n, user tasks, the `test-plan` section |
| `implementation-discipline` | skill | | locked tests, small verified steps, revert after two failed fixes, no suppressions, size budget, typed stop |
| `quality-review` | skill | | how any stage auditor judges a run: checks with evidence, sampled claims, no default to continue; `auditor.sh` loads it by name when the auditor's stage skills include it |

The Thulu flow agents grant MCP tools by explicit list (never a bare
`octopus-api@write`), run their auditor with `quality-review` and more turns
than the default 5, only when the run shows a signal (`"mode": "signals"`, see
`internal/task/audit_signals.go`), and write the spec into body sections other agents read
by slug: `goal`, `context`, `acceptance-criteria`, `interface`,
`key-examples`, `definition-of-done`, `out-of-scope`, `surface`, `risks`,
`assumptions`, `clarifications`, `test-persona`, `bug-behavior`, then
`test-plan` and `user-tasks` from test design.

Skills of the verification, review, testing, readiness and release agents:

| Skill | Agent | What it teaches |
|---|---|---|
| `quality-gates` | `thulu-verifier` | running `thulu-gates` and reading `report.json`, what blocks per check, the four causes of a red test, triaging findings without closing high ones, which `verdict.md` and where a kick_back goes (critical mutants to `test_design`, the rest to `in_progress`) |
| `code-review` | `thulu-reviewer` | spec check per criterion, dimensions as a guide, one refuting subagent per candidate finding, zero findings as a valid result, severities, re-reviews, publishing a `COMMENT` review on the draft PR |
| `agentic-testing` | `thulu-agentic-tester` | the five passes (functional and exploratory, visual, blind user and Nielsen heuristics, interleaved timings, `a11y_scan` and keyboard), k/3 with a named oracle, what blocks, the `ux_review` approval |
| `production-readiness` | `thulu-readiness` | `git fetch` plus `git merge-tree` for freshness (with a temporary object dir where `.git` is read-only), commit rules, forbidden files and secret patterns, a row per criterion and answer, test case runs on the current head and the written authorization a skipped case needs, report consistency, nits, the verdict table per send-back target and the repeated send-back rule |
| `release-verification` | `thulu-release` | watch and batch modes, what went out by `merge_sha` plus reverts, production-safe smoke, the health window with `inconclusive`, rollback, the `deploy_prod` approval before `run_dispatch` |

### flows/

A flow is a whole board. `POST /workshop/apply` with
`{"items": [{"kind": "flow", "name": "thulu"}]}` installs the agents it names
(and their skills) through the same plan as any agent, then creates the board
with its columns, flags, approvals, task types and agents. Agents go first, so
a board never names an agent the same apply failed to write.

```json
{
  "revision": 1,
  "label": "Thulu flow", "labels": {"es": "Flujo Thulu"},
  "board": {"id": "thulu", "max_kick_backs": 2},
  "stages": [{"id": "in_progress", "default_agent": "thulu-developer",
              "labels": {"en": "Development", "es": "Desarrollo"}, "auto_advance": true}],
  "task_types": [{"id": "release", "label": "Release",
                  "skips": ["in_progress"], "agents": {"release": "thulu-release"}}]
}
```

- A stage takes every field of `GET /boards/{id}/stages` plus `labels` per
  locale; without a label it uses `stage.labels.<id>` from the i18n catalog.
  Approvals take `labels` too. The board's texts are written in the locale of
  whoever applies it.
- `default_agent` is the column's agent; a type's `agents` overrides it for
  tasks of that type on that column, and `""` leaves the column empty for that
  type. A task resolves, lowest first: column, project template, type, what
  the request names.
- `go test ./internal/workshop/` fails when a flow names an agent the catalog
  does not have, or puts one in a stage outside its `valid_stages`.
- The board keeps `flow`, `flow_revision` and `flow_hash`. The hash covers
  what makes the board work (flags, agents, approvals, types and their skips)
  and leaves out labels, colors and icons, so renaming a column is not an
  edit. With the hash unchanged and a higher revision the plan offers an
  update; with it changed, a conflict (keep, replace or copy). Update and
  replace keep each column's and type's label and color.
- The board id is `board.id`; when another workspace already holds it, the
  plan picks `<id>_<workspace>` and warns. A copy gets the next free suffix.
- An update that drops a column with tasks in it is blocked until those tasks
  move.
- Skipping an agent in the plan leaves its column without a default and warns.
- `thulu export`/`import` carry the board with its default agents, type agents
  and flow origin, like any other board.

| Flow | Columns | Types |
|---|---|---|
| `thulu` | backlog, refinement, test design, development, verification, AI review, agentic testing, human review, release, done; at most 2 send-backs | `feature` (default), `bug`, `chore` (skips test design and agentic testing), `release` (backlog to release, run by `thulu-release`) |
