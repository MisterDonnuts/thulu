# Skills: the knowledge library

A skill is a `prompt.md` with a methodology, criteria or a rubric, plus a
`skill.yaml` with its metadata. Prompts only, no executable script: if a unit
of work needs to run something (a browser, a screenshot, an external LLM),
that goes in `tools/` as an MCP service.

## Where they live

There are three sources, and none of them is a folder of the repo:

| Source | Where | Who edits it |
|---|---|---|
| Workspace skills | rows in Postgres, one library per workspace | a workspace admin, in `/skills/` or through `PUT /skills/{name}/files/...`; they also come in from the Workshop, by importing a JSON exported from another workspace, or with `thulu agents import -workspace <id> <tree or pack>` |
| System skills | `internal/builtin/system-skills/`, embedded in the binary | changed in the code and shipped with the release; the operator can patch them with `PUT /system/skills/{name}` |
| MCP manuals | `tools/<name>/skill.md` | changed in the code, next to the MCP they document |

The Workshop (a button on `/agents/` and `/skills/`) offers agents and skills
that ship with the release, in `internal/builtin/workshop/` (format in the
`internal/builtin` README). What gets installed is copied into the workspace
and becomes its own; if nobody edits it, a release with a newer revision
offers it as an update. "Export", in the editor of a skill or an agent,
downloads a JSON with it and the skills it uses, which another workspace
imports from the same Workshop.

A new workspace starts with an empty library. Each workspace edits its own
without touching the rest. Every change is kept in the skill's history
(`/skills/{name}/revisions`) and can be restored.

System skills carry two more fields in `skill.yaml`: `version` and, optionally,
`mcp`. The daemon updates them at startup when the embedded version is higher
than the stored one, so an operator's patch lasts until the next release that
bumps the version. The `octopus-` prefix is reserved for them: a workspace
cannot create a skill with that name. There are nine:

| Skill | `mcp` | `enforcement` |
|---|---|---|
| `octopus-run` | none (every run) | `always` |
| `octopus-task-workflow` | `octopus-api` | `on_demand` |
| `octopus-refinement` | `octopus-api` | `on_demand` |
| `octopus-observability` | `octopus-api` | `on_demand` |
| `octopus-test-cases` | `octopus-api` | `on_demand` |
| `octopus-browser-evidence` | `thulu-browser` | `on_demand` |
| `octopus-evidence-library` | `thulu-browser` | `on_demand` |
| `octopus-sandbox` | `thulu-sandbox` | `on_demand` |
| `octopus-token-optimizer` | none (`when: token_optimizer`: only runs whose Token optimizer is on) | `on_demand` |

## Anatomy of a skill

```
<name>/
├── skill.yaml        ← name, description, type, enforcement, requires
├── prompt.md         ← Knowledge: methodology, criteria, rubric
├── scripts/…         ← optional: listed with their path as "Scripts"
└── references/…      ← optional: any other file is listed with its path
                        at the end of the skill, as "Other files"
```

In `/skills/`, the description and the delivery mode are changed from the
skill's header (`PATCH /skills/{name}`), without editing `skill.yaml` by hand.

```yaml
name: ux-analysis
description: What it covers and when to use it. It is all the model sees before opening it.
type: knowledge
enforcement: on_demand
requires: []
```

Every skill is `type: knowledge`. `requires` shows in the catalog but the
daemon does not resolve it: granting a skill does not grant the ones it names.

### `enforcement:` how the skill reaches the model

With an engine that discovers skills on its own (`Capabilities.Skills`,
currently claude-code) the run **does not paste the text into the prompt**: it
writes each skill as `~/.claude/skills/<name>/SKILL.md` and the engine opens
the one it needs. The `description` is all it sees until then; if it is
vague, the skill is invisible. The daemon parses `skill.yaml` when it builds
the bundle and leaves `description` and `enforcement` in
`skills/<name>/.skill-meta`, one line each, which is what `skills-native.sh`
reads.

That does not work for every skill, hence the field:

| Value | What it does | When |
|---|---|---|
| `on_demand` (default) | written as a file; the model opens it if it applies | domain reference |
| `always` | pasted whole into the prompt | rules that hold on **every** turn |

The test for `always` is not "it matters", since nearly all of them do. It
is: **if the model never opens it, does the process break silently?** Among the
system skills, the only one is `octopus-run`: without it an agent moves its own
task to another stage or leaves its report where nobody reads it. A rule the
model has to *choose* to read is not a rule.

With an engine without discovery (codex) everything is pasted
into the prompt. `SKILLS_MODE` decides it, and the dispatcher resolves it
against the engine (`internal/task/dispatcher_env.go`); the agent can force it
with `thulu.skills.mode` (`auto`, `inline` or `native`).

A workspace skill named `_base` is handled apart: it does not appear in the
`/skills/` list, travels to every run and is always inline. No release ships
one.

## How a skill reaches a run

For each run, the daemon builds the list with `StageSkills(stage)`
(`internal/types/types.go`):

1. The skills granted to that stage in the agent type (`skills_by_stage`, the
   agent's Skills tab in `/agents/`).
2. The system skills that apply: those with no `mcp` and those whose `mcp` is
   wired on that stage (`mcp_by_stage`). If a workspace skill has the same
   name as a system skill, the run reads the system one.

That list reaches the container as `SKILLS`, and the run bundle
(`/run/bundle`) brings their texts to `/workspace/skills/`, along with
`tools/<name>/skill.md` of each wired MCP. Then the entrypoint:

- `snippets/skills-native.sh` (only with `SKILLS_MODE=native`) writes each
  `on_demand` skill as a `SKILL.md` where the engine looks for them and leaves
  only the `always` ones in `SKILLS`.
- `snippets/skills.sh` pastes `_base`, if it exists, and the skills left in
  `SKILLS`.
- `snippets/mcp-skills.sh` pastes the manual of each MCP in `MCP_TOOLS` under
  a `## Tool: <name>` heading. thulu-browser, thulu-cloud, thulu-figma,
  thulu-github and thulu-sandbox have a manual; octopus-api has none, because
  its system skills cover it.

Agents **cannot modify skills**: the bundle is extracted on every run and
whatever the agent changes in `/workspace/skills/` is lost.

## Stage prompts direct, they are not encyclopedias

The agent type's stage prompts (`prompts/<stage>.md`, editable in `/agents/`)
only coordinate: which reports to produce, with which skill, from which data.
The methodology lives in the skill. That keeps the prompts short (50 to 100
lines).

## The right granularity

A skill is a **coherent domain of competence**, not an output file:
- ✅ `ux-analysis` (covers navigation, friction, action hierarchy)
- ✅ `performance-analysis` (covers Core Web Vitals and related metrics)
- ❌ `scroll-analysis` (too small, it belongs inside ux-analysis)
- ❌ `landing-analysis` (too big, it mixes copy, UX and visuals)

## Rules for new skills

1. **Identify the domain of competence first**, not the current agent's need
2. **Write it independent of the kind of product**: `ux-analysis` should serve
   landings, dashboards and apps
3. **Reference other skills with `[[wikilink]]`** when there is a conceptual
   dependency
4. **Document the expected output format** in the prompt: the skill should be
   self-contained
5. **A new skill born for one agent becomes available to every agent in the
   workspace**
6. **What depends on an MCP goes in the MCP's `skill.md`** or in a system
   skill with `mcp:`, so it travels only when the run can use it
