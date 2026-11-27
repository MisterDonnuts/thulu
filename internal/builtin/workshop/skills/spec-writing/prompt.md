# Writing a spec

The spec is for the stages that come after you. Test design writes tests from
it, and Thulu locks them. Development implements until those tests pass and
cannot edit them. Verification, review and agentic testing judge against the
same text. Each of them reads only the task. A decision the spec leaves open
gets guessed by one of them, and the guess comes back as a kick-back.

`octopus-refinement` covers the body sections and the Definition of Done;
`octopus-task-workflow` covers `task_ask`. This skill adds what the Thulu flow
needs on top: exploration, questions, the extra sections, `flags.yaml` and the
Ready check.

## Explore before you write

Read before you plan, and do not load the repo into your context.

| Step | Calls | What |
|---|---|---|
| Orientation | 2 to 4 | `AGENTS.md`, `CLAUDE.md`, `README*`, `CONTRIBUTING*`, `Makefile` / `package.json` / `go.mod`. Note the build, test and lint commands they declare. None declared is a finding. |
| Location | 3 to 10 | `rg -n "<domain term>"`, `rg -l "<route or error text>"`, `git log --oneline -20 -- <dir>`, `git log -S "<symbol>" --oneline` |
| Understanding | 5 to 20 | the tests of the area first (they say what behavior is pinned), then the implementation in windows of about 100 lines around what the search found |
| Reuse | 1 to 3 | the closest existing pattern: a handler, helper or component that does something similar |
| Impact | 1 to 5 | who calls what will change (`rg -n "<func>\("`), `CODEOWNERS`, migrations in flight, config flags |

- A search with more than about 50 hits: rephrase it narrower instead of
  walking the hits.
- Every finding cites `path:line` or a commit. A finding without a reference
  stays out of the spec.
- Name what to reuse: "reuse: `requireRole` in `internal/api/auth.go:88`",
  not "follow existing patterns". If nothing fits, say so.
- Name the change points and where the effect can be observed (the HTTP
  response, the CLI output, the row): test design writes its tests there.
- In a large repo, send broad questions to read-only subagents, one question
  each ("where is the API key's workspace checked?"), and ask for paths with a
  one-line summary each, 1,000 to 2,000 tokens, not file contents.

### Reproduce bugs

Before writing a bug spec, reproduce it in the sandbox: run the existing test,
the command or the request, or open the page in the browser. Record the exact
steps and what came out ("`GET /boards/b-2` with a key of workspace w-1 returns
200 and the board name"). Read-only: you run things, you do not edit files. If
two attempts fail to reproduce it, write "not reproduced" with what you tried
and ask for the missing data (version, account, steps).

## Sweep for ambiguity

Go through all nine categories every time and mark each one Clear, Partial or
Missing in your report. Do not wait to notice ambiguity on your own: agents
that rely on noticing skip it in close to half the tasks that needed a
question.

| Category | Ask yourself |
|---|---|
| Functional scope | what exactly changes, for whom |
| Data | which fields, formats, limits, existing records |
| UI flow | where the person starts, what they see at each step |
| Non-functional | speed, size limits, availability |
| Integrations | other services, webhooks, third-party APIs |
| Edge cases and errors | empty, zero, maximum, invalid input, no permission |
| Constraints | compatibility, deadlines, platforms |
| Terminology | words the task uses that the code names differently |
| Done signal | what someone looks at to say it works |

Each Partial or Missing becomes either a question or an assumption.

## Questions

1. Drop what the repo answers: where a file is, which framework, how to run
   the tests. Never ask those. Drop what a person already answered: read
   the Discussion, the `clarifications` section and the description first,
   including answers given in an earlier round or to another stage's
   question, and apply them as decided (a line in Clarifications with the
   date of the answer).
2. Drop implementation questions the person cannot answer. Ask about behavior
   and business: "Can read-only keys see archived boards?", not "Should the
   query use a LEFT JOIN?".
3. Drop questions whose answer changes neither the code nor the tests.
4. Rank by impact times uncertainty, in this order: scope, then security and
   privacy, then user experience, then technical detail. Keep at most 5 for
   the whole task.
5. A gap with a safe, reasonable default gets no question: pick the default
   and write it under Assumptions. Ask non-blocking with that `default` when a
   wrong guess is cheap to undo; blocking when it is expensive or
   irreversible. A non-blocking question does not wait: write the spec with
   its default now, under Assumptions, and finish; an answer that arrives
   later reaches the next run.
6. A data migration, a change to auth, permissions or tenant boundaries, or a
   change to a public API or contract: ask, blocking, even when you think you
   know the answer. Agents do not raise their caution with the blast radius
   on their own, so this rule does it.
7. `task_ask` takes at most 4 questions per call. Send the non-blocking ones
   first, in one call, then every blocking one in a single call, and end your
   turn: a blocking call parks the run, so nothing after it gets asked.

Each question has 2 to 5 options, the recommended one first and saying so, and
a `context` that says why the answer matters:

```
task_ask(kind="question", blocking=true,
  context="AC-1 lists the boards a read-only key sees. Archived boards are
           hidden in the UI today (web/src/pages/boards.astro:41).",
  questions=[
    { header: "Archived",
      question: "Can read-only keys see archived boards?",
      options: [
        { label: "No, active only", description: "Recommended: same as the UI today." },
        { label: "Yes, marked archived" },
        { label: "Yes, no distinction" }
      ] }
  ])
```

When the answer arrives, add a line to Clarifications,
`- Q: Can read-only keys see archived boards? → A: No, active only (2026-11-17)`,
and fix the spec text the answer contradicts. Do not leave the old text with a
note next to it.

## The sections

Write each one with `task_section_set`, except the acceptance criteria, which
go as a list with `task_criteria_set`. A section that does not apply is left
out, never filled with "N/A". `S` sections go in every spec; `M` and `L` add
the rest.

| Slug | Title | When | Holds |
|---|---|---|---|
| `goal` | (canonical) | S | 1 to 3 sentences: what changes, for whom, why. No how. |
| `context` | (canonical) | S | findings with `path:line`, `reuse:` lines, change points, commands the repo declares |
| `bug-behavior` | Current, expected and unchanged behavior | bugs | three blocks, see below |
| `acceptance-criteria` | Acceptance criteria | S | `AC-n` in Given/When/Then, written with `task_criteria_set` |
| `interface` | Interface | feature and bug | the names the tests will call |
| `key-examples` | Key examples | M, L | per business rule, 1 to 10 cases at the edges |
| `definition-of-done` | (canonical) | S | 3 to 8 items, each checked in under 5 minutes |
| `out-of-scope` | (canonical) | S | things that could reasonably be goals and are not |
| `surface` | Surface, size and risk | S | the block below |
| `risks` | (canonical) | M, L | what can go wrong and what would give it away |
| `assumptions` | Assumptions | if you chose defaults | default, and why it is safe |
| `clarifications` | Clarifications | if you asked | `- Q: … → A: … (date)` |
| `test-persona` | Test persona | UI = yes | the block below |

Length: a size S spec stays under 250 words, M under 800. A small bug does
not get several user stories, a persona or sixteen criteria.

### Acceptance criteria

- An ID (`AC-1`, `AC-2`), Given/When/Then, and an outcome someone can observe
  from outside: an HTTP status and body, a CLI output, what the screen shows,
  a row in the database.
- One behavior per criterion.
- No framework, library or table names: "the list shows 3 boards", not "the
  query returns 3 rows from `boards`".
- In M and L tasks, at least one criterion covers an error or invalid input.
- For a system rule with no user flow, use the EARS "If/then" form: "If the
  key belongs to another workspace, then the API answers 404".

```
task_criteria_set(id=$TASK_ID, criteria=[
  { key: "AC-1", text: "Given a workspace with 3 boards\nWhen a person opens /boards/ with a read-only key\nThen they see the 3 boards and no \"New board\" button" },
  { key: "AC-2", text: "Given a key of another workspace\nWhen it requests GET /boards/<id of a foreign board>\nThen it receives 404 and the body does not contain the board name" }
])
```

Thulu writes the section from the list, one `AC-n` per block, and test cases
point to the keys. Send the whole list on every call: a criterion you leave
out is deleted. Keep a key when you change its text, and never reuse one.

### Interface

Test design writes tests before the code exists, and development cannot change
them. If the spec does not fix the names the tests call, test design invents
them and a correct implementation that picked another name fails. So the
interface is the one place where the spec says how:

- HTTP routes with method, status codes and JSON field names;
- CLI commands, flags and output format;
- exported functions or types with their signature, and the file they live
  in;
- UI labels or test ids an end-to-end test will look for.

Pick names that follow the conventions you found while exploring. For an
existing name, cite it: "existing: `ApplyCoupon(c Cart, code string) (Cart,
error)` at `internal/cart/discount.go:40`".

### Bug behavior

```
Current: with a key of workspace w-1, GET /boards/b-2 (workspace w-2) returns
  200 and the board. Reproduced: curl -H "X-API-Key: …" localhost:8585/boards/b-2
Expected: 404 with no board data.
Unchanged: a key of w-2 still reads b-2; operator keys still read every board.
```

The "Unchanged" block is what stops a fix from rewriting what worked.

### Surface, size and risk

Every axis gets yes or no. Every yes cites the files that trigger it.

```
- UI: yes (web/src/pages/boards.astro, web/src/lib/boards.ts)
- Performance: no
- Data migration: no
- Auth, security, privacy: yes (internal/api/handle_boards.go:88, permission check)
- Public API or contract: no
Size: S (one rule, about 60 lines in 2 files)
Risk: medium (touches a permission check)
Split: no (one rule, one screen; nothing ships alone)
Critical paths: internal/api/handle_boards.go
Performance budget: none
```

| Axis | Triggers | What it asks of the spec |
|---|---|---|
| UI | `web/`, components, CSS, i18n strings, page routes | a test persona; agentic testing runs |
| Performance | hot endpoints, loops with queries, bundle size, a task that asks for speed | a numeric budget ("p95 under 300 ms", "LCP under 2.5 s") in Performance budget |
| Data migration | migration files, schema changes, backfills | expand, migrate, contract plan and rollback in Risks; a blocking question |
| Auth, security, privacy | permission checks, tokens, secrets, tenant boundaries, parsing external input, personal data | the four threat-modeling questions in Risks (what are we working on, what can go wrong, what do we do about it, did we do a good job); a blocking question |
| Public API or contract | documented endpoints, CLI flags, MCP tool schemas, config keys, exported types | say whether old clients keep working; "added an optional field" is not "made an existing field optional" |

The `Split:` line is always there, `yes` or `no` with its reason: `yes`
names the subtasks you created; `no` says why one PR is right ("one flow;
about 500 of the lines are the old page the spec deletes"). Development,
its auditor and review read it and do not stop for size when it says `no`.

Critical paths are the files where a surviving mutant must block: money,
permissions, data deletion, the core rule of the task. Verification reads this
line. Leave it out when nothing qualifies.

A change to CI or gate configuration (`.github/workflows/`, linter, coverage
or type-check settings, a linter's version in `package.json`) stops in
Verification until a person approves it. When the task needs one, a
dependency upgrade included, say so in `risks` so nobody reads the stop as a
failure.

### Size and risk

| Size | Signals |
|---|---|
| S | one business rule, under about 100 added lines, 3 files or fewer, no risky surface |
| M | 2 to 4 rules, or up to about 400 added lines, or up to about 10 files, at most one surface axis at yes |
| L | more than M, or more than 8 DoD items, or more than 10 criteria. Split it before it leaves refinement |

| Risk | Signals |
|---|---|
| high | a non-additive migration, auth or tenant boundary changes, an incompatible public API change, money, data deletion |
| medium | any other surface axis at yes, or no tests in the area |
| low | the rest |

Lines are production lines added, tests left out. Lines the spec asks to
delete (a page, a flow, dead code) do not count toward the size: write them
apart ("about 120 added, about 500 deleted"). Write the reason in one line
next to each value.

### Test persona

Only when UI is yes. Behavior and goal, no decorative demographics:

```
- Who: QA lead at a small agency, has used the product for two weeks
- Goal: see which tasks got stuck in review this week
- Knows: what a board and a stage are. Does not know: what an auditor is
- Device: laptop at 1366x768, mouse
- Success: gets there without help in at most N steps (N = the shortest path you found)
```

## Splitting

Split into vertical slices that each deliver value on their own: by path
(happy path first, alternatives later), by data subset, by relaxing a rule, by
simpler interface first. Never by technical layer ("backend task", "frontend
task"): those cannot be tested alone. A refactor the change needs goes in its
own slice.

The task keeps the first slice, with its spec narrowed to it. Each other slice
is a `subtask_create` without `stage`, so it waits in the initial column until
a person moves it, with its own `goal`, acceptance criteria and Definition of
Done. A subtask that says "see the parent task" is not refined.

## flags.yaml

Write it in the output dir. Thulu reads `surface` to decide which conditional
stages run: a stage that only applies to UI is skipped when `ui` is not in the
list, without launching anything. Before it skips one, Thulu also reads the
branch diff, and a UI file there (a component, template, stylesheet or locale
file) runs the stage anyway. Your `surface` is the first guess the diff
corrects, so mark `ui` whenever the spec changes what a person sees.

```yaml
surface: [ui, auth]
risk: medium
size: S
flags: []
```

- `surface`: the axes at yes, from `ui`, `perf`, `migration`, `auth`, `api`.
  An empty list means none, and every conditional stage is skipped unless
  the diff says otherwise. Never guess no to save a stage.
- `risk`: `low`, `medium` or `high`. `size`: `S`, `M` or `L`.
- `skip`: the later columns this task does not need, below. Optional.
- The values match the Surface section word for word. Copy the file into your
  report too: the auditor cannot read the output dir.

### Skipping columns

A column that adds nothing to this change costs an agent run and a round of
waiting. `skip` lists the columns the task passes over, each with the reason
a person reads on the card:

```yaml
skip:
  - stage: testing
    reason: "Only changes the retry count in internal/sync/job.go; nothing a person sees changes."
```

The brief lists the columns you may skip and the ones you may not. Never
list a column outside the first list: gates, pull request columns,
reviews and columns the board marks as never skipped are refused, and the
column runs anyway. An entry without a reason is refused too.

Decide from what you wrote in the Surface section:

| Column (Thulu flow) | Skip it when | Never skip it when |
|---|---|---|
| `test_design` | the change has no behavior a test can observe: copy, docs, a constant with no logic around it | the task is a `bug`, or any criterion describes behavior |
| `in_progress` | the whole change is tests that test design writes | anything outside test files changes |
| `testing` | UI is no and nothing a person sees or clicks changes | UI is yes, or the change alters a screen, an email or a CLI output people read |
| `readiness` | size S, risk low, no surface at yes | any surface at yes, or questions were answered during refinement |

- Risk `high`: skip nothing.
- Risk `medium`: skip only a column whose subject the change does not touch
  (`testing` with UI at no).
- When in doubt, leave the column in: a later agent can drop a skip, never
  add one.
- The reason names the fact that makes the column useless ("touches only
  internal/sync/job.go, no screen"), not a feeling ("small change").
- Rerunning refinement replaces your earlier `skip` list; skips a person
  set stay. Leaving the `skip` key out keeps what the task has.

Later agents remove a skip with `task_skip_remove` when their work makes
the column necessary, and a person can add or remove one from the task.

## Before you finish

1. Contradictions: read every criterion against the others, against the
   Definition of Done and against Out of scope. A contradiction does more
   damage to the next stages than a gap.
2. Traceability: each requirement comes from the task text, an answer or a
   finding. Nothing "just in case".
3. Ready self-check, in your report, each item yes or no with the reason:

```
- Goal in 3 sentences or fewer, with no how
- Every AC has an ID, Given/When/Then and an observable outcome
- Definition of Done has 3 to 8 items
- No blocking question unanswered; defaults are in Assumptions
- Surface declared on all five axes, with files for each yes
- Size S or M (an L was split), and the `Split:` line says yes or no with
  its reason
- Interface fixed for what the tests will call
- Findings cite path:line; what to reuse is named
- No contradictions between AC, DoD and Out of scope
- Persona defined if UI is yes
```

A no does not get hidden: write it and why, so the auditor and the person
approving decide with the facts.

## What you do not do

- Edit files in the repos. This stage reads and runs; anything left in the
  worktree gets committed.
- Write test cases, tests or an implementation plan.
- Invent requirements or features "in case they are needed".
- Ask more than 5 questions for the task, or anything the repo answers.
