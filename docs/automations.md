# Automations

An automation is a trigger plus a short list of steps. It runs on a schedule, on
an event, when a metric crosses a threshold, or when somebody presses Run.

The page is `/automations/`. The recipe is YAML, stored as text, parsed on save
and again on every run. Everything lives in one workspace: an automation created
in workspace A is invisible to B, and its steps can only touch A's boards,
tasks, projects and credentials.

```yaml
name: nightly error sweep
trigger:
  schedule: "0 3 * * *"
  tz: UTC
policy:
  concurrency: forbid
  timeout: 10m
  max_agent_runs_per_day: 20
steps:
  - id: errors
    query:
      source: metric
      environment: prod
      metric: http_5xx_rate
      window: 1h
      agg: avg
  - id: gate
    condition: steps.errors.value > 0.02
  - id: ticket
    task:
      title: "5xx rate at {{ steps.errors.value }}"
      board: incidents
      fingerprint: "5xx-{{ trigger.fired_date }}"
      on_duplicate: comment
```

The boards, stages and agents in this page's examples (`incidents`,
`engineering`, `review`, `reviewer`) stand for ones the workspace created. A
new workspace has one board (Backlog → In progress → Done) and no agents.

---

## The shape of a recipe

A recipe is linear. A step runs, the next one runs, the run ends. The only
control flow is `condition` (stop here), `when` (skip this step) and `fanout`
(repeat these steps per item).

Work that needs judgement, several turns or a person goes to a **task**, which
is what the `task` and `mention` steps are for. The board already has stages,
assignments, comments, agents and an audit trail, and a second workflow engine
beside it would be two half-implementations of the same thing.

So a recipe is measured in seconds, and `policy.timeout` is capped at one hour.

---

## Triggers

Exactly one per automation.

| Trigger | Field | Fires |
|---------|-------|-------|
| Schedule | `schedule` + `tz` | on a cron expression |
| Event | `event` + `filter` | when a matching occurrence is published |
| Metric | `metric` | when a metric crosses a threshold |
| Manual | `manual: true` | only from the Run button or `POST /automations/{id}/run` |

### Schedule

Five fields, Vixie semantics, plus `@hourly`, `@daily`, `@weekly`, `@monthly`,
`@yearly`. Step values (`*/15`), ranges (`1-5`), lists (`1,15`) and names
(`mon`, `jan`) all work. When both day-of-month and day-of-week are restricted
they are ORed, the same as crontab.

`tz` is an IANA name and defaults to UTC. Firing times are computed by walking
absolute time, so a daily 02:30 job fires once on the day a DST jump skips 02:30
(at the next valid wall clock) and once on the day it happens twice.

The editor's Validate button prints the next five firings. That is the answer to
"did I write the cron I meant to".

`starting_deadline` decides what happens when the daemon was down at the firing
time: a run older than the deadline is skipped rather than caught up, so a
weekend of downtime does not produce sixty backfilled runs on Monday morning.

### Event

```yaml
trigger:
  event: github.pull_request_review_comment
  filter: |
    !trigger.payload.sender_is_bot
    && trigger.payload.task_id != ""
```

The catalog:

| Event | Published when |
|-------|----------------|
| `github.pull_request_review_comment` | a review comment lands on a PR |
| `github.pull_request_review` | a review is submitted |
| `github.issue_comment` | a comment lands on an issue or PR |
| `github.pull_request` | a PR is opened, closed, synchronized… |
| `github.push` | commits are pushed |
| `github.check_suite` | a check suite completes |
| `task.created` | a task is created: by a person, a subtask, or an automation (not by cloning or forking a task) |
| `task.stage_changed` | a task enters a new stage: moved by hand, auto-advanced after an agent run, released by an approval, or kicked back (not when deleting a stage moves its tasks out) |
| `task.failed` | an agent run fails |

GitHub events arrive through the App webhook, already verified. The payload is
flattened into the fields a recipe reads (`repo`, `pr_url`, `pr_number`,
`comment`, `comment_by`, `sender_is_bot`, `branch`, `review_state`, …) and, when
the pull request belongs to a card, `task_id`, `task_title` and `task_stage`.

A GitHub event belongs to the workspace that claimed the App installation it
came through, and only that workspace's recipes see it. `task_id` is looked up
among that workspace's cards only. A delivery from an installation no workspace
claimed is acknowledged to GitHub and dropped, and an automation with no
workspace receives no events at all. The
one exception is a self-hosted install (`OCTOPUS_SAAS_MODE` off) with a single
workspace: there an unclaimed installation's events go to that workspace until a
second workspace exists.

`sender_is_bot` exists because of the obvious failure mode: a recipe that wakes
an agent on every comment answers its own comment, forever. Filter on it.

A subtask's `task.created` carries `parent_id`; one opened by a recipe carries
`source: automation` and `automation_id`. A stage change carries `from` and `to`.

`policy.debounce` collapses a burst. The first event for a key opens a window;
the events that land inside it are held, and when it closes the recipe runs
once. `trigger.payload` is the newest event, `trigger.payload.events` holds all
of them (up to 50, oldest first) and `trigger.payload.event_count` how many
arrived, so five review comments on the same PR wake one run that can read all
five. The window is fixed from the first event and does not stretch while
events keep coming. The key is the PR URL for GitHub events and the task id for
board events. Held windows are stored in Postgres and survive a restart. A
window that closes while another run of the recipe is still going
(`concurrency: forbid`, the default) waits for it instead of being skipped, and
the events that arrive meanwhile join it.

Two guards stop recipes from feeding each other forever. A recipe never fires on
an event about a task it opened itself, and a task opened by a chain of three
recipes (one opens a task, its `task.created` fires another, and so on) fires no
fourth: that event leaves a `skipped` run saying so. Both guards cover every
event about such a task, not only its `task.created`: a stage move, a failed run
or a drag by hand carries the same `automation_id` and `automation_depth`, so a
recipe on `task.stage_changed` whose own task later auto-advances into the stage
it watches does not fire again.

A `filter` that fails to evaluate (a misspelled function, a field of the wrong
type) leaves a `skipped` run with the evaluation error, so a broken filter shows
up in the history instead of a recipe that silently never fires.

### Metric

```yaml
trigger:
  metric:
    environment: prod
    metric: checkout_latency_p95
    window: 15m
    agg: avg      # avg | max | min | sum | last
    op: ">"       # > >= < <=
    threshold: 800
    every: 5m     # sweep interval, minimum 1m
```

`environment` is the environment's ID, not its name; the editor's Environment
picker writes it. Save refuses an ID that does not exist in the recipe's
workspace. The metric must exist in the custom-metrics catalog for that
environment.

The trigger latches, the way an alerting product's alert state does: it fires on
the crossing and stays quiet until the value comes back under, so an incident
that burns for an hour opens one ticket rather than twelve. A window with no
points at all is treated as no information rather than as "recovered", so a gap
in collection does not re-arm a still-burning incident.

The run reads the crossing at `trigger.payload`: `environment`, `metric`,
`value`, `threshold`, `op`, `window` and `agg`.

---

## Steps

Every step has an `id` (`[a-z0-9][a-z0-9_]{0,31}`), exactly one action, and two
optional modifiers: `when` (an expression that skips the step when false) and
`continue_on_error` (the step's failure is recorded on its own row and the run
carries on, ending `success`).

A step's result is readable by the ones after it as `steps.<id>.<field>`.

### `condition`

Ends the run early when the expression is false. The run is `success` and its
reason names the condition that stopped it, because a guard reporting "nothing
crossed the threshold" is not a failure. The steps after it never run and leave
no rows.

Inside a fanout it ends that iteration instead of the run: one error group being
uninteresting says nothing about the next one.

### `query`

Reads Thulu's own data. Two sources, both closed sets of filters rather than a
query language, for the same reason the metric catalog is closed.

```yaml
query:
  source: tasks
  board: engineering
  stage: review
  tag: urgent
  project_id: abc123
  with_pr: true
  open: true
  stale_for: 48h
  limit: 20
```

```yaml
query:
  source: metric
  environment: prod
  metric: http_5xx_rate
  window: 1h
  agg: avg
```

`open: true` leaves out tasks in a terminal stage (done, archived). Without it a
query reads every task that matches, closed ones included, which is what a
report wants and what a "how many are still pending" guard does not.

`tasks` returns `count` and the rows under `json.tasks`; `metric` returns
`value` and `has_data`. An environment that does not exist, or belongs to
another workspace, is an error rather than `has_data: false`. A metric with no points in the window reports
`has_data: false` with a zero value, so a threshold check should read
`steps.x.has_data && steps.x.value > 0.02` rather than the value alone.

### `script`

Runs a shell command in a sandbox the run owns, and parses stdout as JSON into
`steps.<id>.json` when it is JSON. `stdout`, `stderr` and `exit_code` are always
there.

A non-zero exit does **not** fail the step. A script that exits 1 to mean "I
found three problems" is a normal thing to write, so the recipe decides what the
code means: `condition: steps.check.exit_code == 0`.

```yaml
script:
  run: |
    curl -s "$API/health" | jq '{errors: .error_count}'
  env:
    API: "https://api.example.com"
  timeout: 2m      # default 2m, ceiling 10m
```

The sandbox image ships node, python3, git, curl and jq, so a longer script can
be a heredoc into `python3 -` rather than a pile of shell. It starts empty: no
repo checkout, no project worktree, and nothing left over from the previous run.
Anything the script needs comes in through `env` or over the network.

There is no `image` field. The sandbox image is chosen once when the daemon is
wired, and a per-step override would have been a knob the runtime ignores. A
recipe that needs a different toolchain opens a task, whose stage runs the agent
image it is configured with.

The sandbox is owned by `auto:<run id>` and released when the run ends. No
throwaway task is created to hold it.

### `http`

```yaml
http:
  method: POST
  url: "https://hooks.example.com/notify"
  headers:
    Content-Type: application/json
  credential: slack-webhook
  body: '{"text": "{{ steps.errors.count }} stale PRs"}'
  timeout: 30s
```

`credential` names an entry in the credential manager; the secret is injected at
call time, never stored in the recipe and never written to the run history. The
response lands as `status_code`, `text` and, when the body parses, `json`. A
4xx or 5xx fails the step with the body still readable, so a
`continue_on_error` step can report what the other side complained about. The
timeout is capped at 30 seconds and the body at a fixed size.

### `mcp`

```yaml
mcp:
  server: thulu-github
  tool: pr_list
  args:
    repo: "{{ trigger.payload.repo }}"
    state: open
```

The call carries a key scoped to `automation:<automation id>:<run id>`, so an
audit log answers which recipe made it. The key goes only to a server whose
registration is marked `forwards_credential`, the same rule the MCP proxy
applies to agents. The step reaches legacy SSE servers with no credential of
their own; a server registered as `transport: http` or `auth: credential` is
refused with an error that names it.

### `task`

Opens a card. This is how a recipe hands multi-turn work to the board.

```yaml
task:
  title: "Address review comments on #{{ trigger.payload.pr_number }}"
  body: "{{ trigger.payload.comment }}"
  board: engineering
  stage: development
  type: bug
  tags: [automated, review]
  priority: high
  project_ids: [abc123]
  fingerprint: "pr-{{ trigger.payload.pr_number }}-review"
  on_duplicate: comment     # comment | skip | create
```

**`fingerprint` is the field that decides whether this automation is usable.**
Without it, an hourly recipe that finds the same broken thing opens twenty-four
cards a day for one problem. With it, the first firing creates the card and the
rest either comment on it (`comment`), do nothing (`skip`), or deliberately open
another (`create`). The claim is atomic in Postgres, so two runs racing produce
one task.

The fingerprint is a template, which is how the window is chosen. End it with
`{{ trigger.fired_date }}` for one card per day; leave it fixed for one card per
problem, however long that problem lasts.

### `mention`

Posts a comment on an existing task and wakes an agent on it. `agent` names
which one; without it, the agent assigned to the task's current stage is woken.
A task whose stage has no agent (backlog, a terminal stage) gets a plain comment
and wakes nobody.

```yaml
mention:
  task_id: "{{ trigger.payload.task_id }}"
  agent: reviewer
  body: "New review comment: {{ trigger.payload.comment }}"
```

### `fanout`

Repeats its nested steps over a list.

```yaml
- id: stale
  query: {source: tasks, stage: review, stale_for: 48h}
- id: each
  fanout:
    over: steps.stale.json.tasks
    as: pr
    max_items: 10
  steps:
    - id: nudge
      mention:
        task_id: "{{ pr.id }}"
        agent: reviewer
        body: "This has been in review for two days."
```

`max_items` is required and capped at 50. A fanout over a query whose result
grew overnight is the classic way an automation opens four hundred tasks, and
the step's row says so when it trims: `9 item(s) (capped at max_items=2)`. The
result carries `items` (how many iterations ran) and `truncated`.

---

## Expressions

Used by `condition`, `when` and the event `filter`. Hand-written Pratt parser,
no third-party evaluator, no access to anything but the run's own context.

Operators: `== != < <= > >=`, `&& || !`, `+ - * / %`, `in`, parentheses,
`a.b.c`, `a[0]`, string and number literals, `true`/`false`/`null`.

Functions: `size(x)`, `contains(a, b)`, `lower(s)`, `upper(s)`,
`default(v, fallback)`, `int(v)`, `string(v)`, `matches(s, regex)`,
`slice(list, n)`, `join(list, sep)`.

Context:

- `trigger.kind`, `trigger.fired_at` (RFC 3339), `trigger.fired_date`
  (`2026-09-17`), `trigger.payload.*`
- `steps.<id>.*` for every step that already ran
- `vars.*`, from the spec's `vars` block
- `run.id`, `run.automation_id`, `run.workspace_id`
- `item` inside a fanout, or whatever `fanout.as` renames it to

Templates in string fields use `{{ ... }}` with the same expression language.
An unknown name is an error at validation time, not an empty string at 3am.

---

## Policy

```yaml
policy:
  concurrency: forbid          # forbid (default) | allow | replace
  starting_deadline: 15m
  debounce: 2m
  timeout: 10m                 # default 10m, hard cap 1h
  max_agent_runs_per_day: 20
  history_limit: 200           # default 200 runs kept
```

`concurrency: forbid` refuses to start a second run while one is in flight and
says why on the run that was refused (`a previous run is still going (1 in
flight)`). `replace` cancels the one in flight and starts the new one. `allow`
lets them overlap, which only makes sense for a recipe that writes nothing.

The same gate applies to the Run button. Clicking it twice does not start two
runs; the catch-up deadline does not apply to a manual run, since the firing is
happening exactly now by definition.

`max_agent_runs_per_day` counts the steps that can spend money: a task step
whose task enters a stage with an agent assigned, and a mention that wakes an
agent. A task filed into the backlog, or a mention on a stage with no agent,
wakes nobody and costs nothing, and still goes through once the budget is
spent. When the budget is spent the run
ends `skipped`, not `failed`, with the ceiling named in the reason: a working
limit and a broken recipe must not look the same on the runs list.

---

## Runs

Every run keeps its steps: what each one did, what it returned, how long it
took, and why it was skipped when it was.

| Status | Means |
|--------|-------|
| `running` | in flight |
| `success` | finished; individual steps may still have failed under `continue_on_error` |
| `failed` | a step failed and the recipe did not say to continue |
| `timeout` | the recipe ran past `policy.timeout` and was cut off |
| `skipped` | a guard stopped it: concurrency, deadline, condition, budget, a filter that failed to evaluate, a chain of recipes too deep |
| `canceled` | stopped by hand or replaced |

Each run also counts `agent_runs`, the steps that actually woke an agent. It is
the column on the runs list and the number `max_agent_runs_per_day` is measured
against. A task step that hits its fingerprint and comments instead of opening a
card costs nothing, so deduplication shows up directly as a zero there.

`history_limit` trims older rows per automation.

Suspending an automation clears its next run: a suspended recipe that still
advertises a firing time is a UI that lies.

---

## API

| Method | Path | Role |
|--------|------|------|
| `GET` | `/automations` | developer |
| `GET` | `/automations/{id}` | developer |
| `GET` | `/automations/{id}/runs` | developer |
| `GET` | `/automations/{id}/runs/{runId}` | developer |
| `POST` | `/automations` | admin |
| `PUT` | `/automations/{id}` | admin |
| `PUT` | `/automations/{id}/enabled` | admin |
| `DELETE` | `/automations/{id}` | admin |
| `POST` | `/automations/validate` | admin |
| `POST` | `/automations/{id}/run` | admin |
| `POST` | `/automations/{id}/runs/{runId}/cancel` | admin |

Reading a recipe is a developer's business. Changing one, running it by hand and
stopping a run are an admin's, because an automation spends model credits and
writes to somebody's board. Validate stores nothing and still carries
`flows:write`: gating it below the role that can save would put a write
permission within reach of a developer key.

API keys need the `flows` resource: `flows:read` to list, `flows:write` to
create, edit, run or delete. An automation is a workflow definition the way a
board is, so it groups with flows rather than tasks; the tasks it opens are the
board's business.

`PUT /{id}/enabled` is a separate endpoint on purpose. Turning something off
must not require sending its recipe back, which is how a stale editor tab
silently reverts somebody else's edit.

Every route answers `503` with `automations are not enabled on this install`
when the store is not wired, so the UI can tell "off here" from "broken".

---

## Editor

The recipe editor validates without storing. A valid spec returns the parsed
name, the trigger kind, the parsed recipe and the next five firings; an invalid
one returns the first error with the field that caused it. A YAML problem names
the line and the key ("line 2: the key "triger" does not go here").

Validate and Save also look up what the recipe names, in its workspace: boards,
stages on that board, agents, projects and environments. A typo fails while
somebody is looking at it, with the message the run would have given at 3am.
These fields take no `{{ }}` template: the engine renders templates only in
text fields (titles, bodies, fingerprints, `task_id`, URLs, scripts, MCP args)
and hands boards, stages, agents, projects and environments to the task store
as written, so Validate and Save refuse a template there instead of letting
every run fail with "unknown board".

Above the YAML, a guided editor sets the trigger (schedule, event, metric,
manual) and its fields, and pickers for boards, stages, agents, environments
and projects insert the ID at the cursor. The header shows the timezone the
schedule fires in, UTC when `tz` is absent.

"Run with test data" runs an event or metric recipe with a sample payload for
its trigger, editable before it is sent, the same `POST /automations/{id}/run`
with a `payload`.

Run reasons and step errors are stored in every language the daemon has and
read back in the reader's. Each step shows a summary ("Created the task
…" with a link) and its raw output in a collapsible block. Templates for the common shapes
(error guard, PR review sweep, answering review comments, a script that decides)
come from `web/src/lib/automation-templates.ts`, and a Go test parses every one
of them, because a template that does not parse is worse than no template.

---

## Files

| Path | What |
|------|------|
| `internal/automation/automation.go` | spec types and validation |
| `internal/automation/cron.go` | the cron parser |
| `internal/automation/expr.go` | the expression language |
| `internal/automation/engine.go` | running one recipe |
| `internal/automation/steps.go` | the step kinds |
| `internal/automation/scheduler.go` | due sweeps, event delivery, metric sweeps |
| `internal/automation/store.go` | Postgres tables and migrations |
| `internal/api/automation_ports.go` | adapters to tasks, MCPs, sandboxes, metrics |
| `internal/api/automation_events.go` | publishing GitHub and board events |
| `web/src/pages/automations.astro` | the page |
