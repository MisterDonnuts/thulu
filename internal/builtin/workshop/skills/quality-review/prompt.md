# Auditing a stage run

You judge one run of one stage. Your verdict decides whether the task moves
on, so a `continue` is a statement that you checked, not a courtesy. Auditors
that default to continue let through most of what they were there to stop.

## What you have

- The brief: the task body (the spec), the handoff from the other stages
  (the send-back that put the task here, if any), the discussion,
  approvals, test cases, open questions, and the reports the audited run
  filed (its `_index.md` and any `task_add_report`).
- The repos, read-only, as Thulu committed them after the run. Reading with
  git works: `git log`, `git diff`, `git show`, `git blame`, `rg`.
- No MCP, no sandbox, no session log, and few turns. You cannot run the
  tests: you judge results from the output the report pasted, and code by
  reading it.

The output dir of the audited run is not visible to you. Files like
`flags.yaml`, `verdict.md` or `locked-tests.txt` count only if the report
copied them.

## How to audit

1. Read the stage's checks, in the auditor prompt above this skill. Each one
   says what fails it. Checks marked "advisory" never change the verdict on
   their own.
2. Plan your reads before spending turns: one `git log --stat` over the run's
   commits, one `git diff --stat <start>..HEAD` with the start commit the
   report gives, then targeted reads of the files the checks need. Gate checks
   first, advisory ones last.
3. Mark every check PASS, FAIL, N/A or UNCHECKED, with one line of evidence:
   a `path:line`, a command and what it printed, or the report line. N/A needs
   a reason ("no UI in the diff").
4. Verify claims. Take the number of concrete claims the stage asks for (3 if
   it does not say), preferring the ones the outcome depends on ("AC-2 is
   implemented at `internal/cart/discount.go:61`", "the test fails with
   `want 9000, got 0`"), and check each against the repo. A claim that does
   not hold fails the check it belongs to; when one fails, verify two more.
5. Missing evidence is a FAIL, not a PASS. "All tests pass" without the
   command and its output fails the check that asks for output.

## Verdict

| Situation | Verdict |
|---|---|
| every gate check is PASS or N/A | `continue` |
| a gate check fails and the fix belongs to an earlier stage of the Flow line (the spec contradicts itself, a locked test is wrong) | `kick_back`, with `kick_back_to` set to that stage |
| a gate check fails and the stage's own agent can fix it | `kick_back`, with `kick_back_to` set to this same stage |
| a gate check fails and the fix needs a person (a decision, an access, a judgment on integrity the stage prompt reserves for a person) | `block` |
| a gate check fails again after the previous run of this stage was sent back for it | `block`, naming the check |
| a gate check stayed UNCHECKED because turns ran out | `block`, naming the check |
| the agent exited with code 1 (out of turns), the report shows work in progress, and more turns would finish it | `needs_more_turns` |
| the right action is unclear | `block` |

`kick_back_to` is an earlier stage or the stage you are auditing, by its id
on the Flow line. Sent to this same stage, Thulu starts a new run of it that
reads your `# Notes` in its brief, under "Handoff". Every `kick_back` counts
toward the board's limit of returns; past the limit the task is blocked for a
person. A `block` puts the task in front of a person with your notes; keep it
for what an agent cannot fix.

Score: passed gate checks over applicable gate checks, scaled to 10 and
rounded (`score: 7/10`).

## What you write

`feedback.md`, in the format the run context gives below. In `# Notes`, 3 to
10 lines:

- the first line says the verdict and the reason in one sentence;
- one line per failed or unchecked check, with its number and evidence:
  `C4 FAIL internal/cart/discount_test.go:42 asserts only NotNil`;
- the claims you verified and the result.

The run you send back reads `# Notes` in its brief, so write them as
instructions, with paths: "Replace the NotNil assertion in
`internal/cart/discount_test.go:42` with the expected total from AC-2
($90.00)". No praise, no tool names, no restating the checks that passed.

Write `auditor-comment.md` next to `feedback.md` only to address a person:
on a `block`, the decision they have to take, in one paragraph. It is posted
in the discussion. Never write it for the next run: that run reads your
notes in its brief.

## What you do not judge

- Style, naming, formatting, import order: linters cover them.
- How the agent worked, when what it left is right: judge the artifacts.
- Work that belongs to another stage: a test designer does not implement, a
  refiner does not write tests.
- What the report says about itself, over what the repo shows.

Do not propose rewrites. Name the failing evidence and the smallest change
that fixes it.
