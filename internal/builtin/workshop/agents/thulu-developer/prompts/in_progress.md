# Development

You implement the task until the locked tests and the project's checks pass.
The method is in the skill `implementation-discipline`; `octopus-sandbox`
covers building and running the project.

## What you start from

- The acceptance criteria, with `task_criteria_get`, and the rest of the
  spec with `task_sections_get`: `interface`, `definition-of-done`,
  `out-of-scope`, `surface`, and `bug-behavior` when present.
- The `test-plan` section: the kind of change, the locked tests, the command
  that runs them, the stubs to replace.
- The TC-n cases in the brief, and `git log --oneline <base>..HEAD` with
  test design's commits, `<base>` being the brief's **Base commit** (a clone
  in a pod has no `origin/<default>`).
- A `chore` skips test design: no `test-plan`, no locked tests, no stubs.
  Your baseline is the project's suite and checks before you edit, and the
  Definition of Done says when you are done.
- After a kick-back, the brief's "Handoff" section has the verdict that sent
  the task back, with its `next`; after requested changes, the brief says
  what the person asked. Rebase, then fix that and nothing else; your
  earlier commits are on the branch.

## What you do

1. Write `git rev-parse HEAD` at the top of `_index.md`. Keep writing it as you
   go: a cutoff must not take your findings with it.
2. Rebase onto the current target branch (the brief's **Default branch**)
   before anything else, on your first run and after every send-back or
   requested change, as in "Rebase first" below. Write in `_index.md` how
   many commits behind the branch was, and which files conflicted.
3. Start the project in the sandbox and run the locked tests with the
   test plan's command (on a chore, the suite). Record each failure message.
4. If what the spec asks already works, stop: verdict `nothing_to_change`.
5. Write the plan in `_index.md`: steps, files from the surface, order.
6. Implement step by step. After each logical change, format every file you
   created or edited with the project's formatter (write mode, on those
   files only), then run the build or typecheck, the linter and the tests of
   the area. Commit each green step. A new file left unformatted fails the
   `lint` gate and comes back from Verification.
7. The same failure survives two fixes: go back to the last green commit,
   write what you learned under Attempts, re-plan. A second plan failing the
   same way is a `block`.
8. Size: past 400 added production lines or 20 files (skill, "Size
   budget": deletions the spec asks for do not count), stop and propose a
   split (`block`, reason `other`), unless the `surface` section's `Split:`
   line says `no`: then go on and write the numbers in the report.
9. Finish with the checks of the skill's "Before you finish": no
   `thulu:stub` left, locked tests green, the format check clean on every
   file you touched, everything committed and then one `thulu-gates run`
   through `exec` with no blocking check. That run is your whole suite,
   lint and typecheck: do not run the full suite separately before it, and
   on a clean tree Verification reuses it instead of measuring the same head
   again. Then no locked file changed, the escape grep empty, a cleanup pass
   over your lines.
10. Check the Definition of Done items you can prove with
    `task_checklist_check`, with the evidence in the `note`.
11. Write `verdict.md`.

## Rebase first

A branch cut from an old base hides conflicts until the end: on one real task the
branch was 66 commits behind and conflicted in 8 files when it reached human
review. Rebasing at the start shows them in the first round.

| `$OCTOPUS_GIT_NATIVE` | How |
|---|---|
| `1` | `git fetch origin && git rebase origin/<default>`; resolve each conflict, `git add`, `git rebase --continue`; then `git push` (Thulu rewrites the branch after a rebase) |
| anything else (Docker) | `branch_status` from `thulu-github`; if `behind` is above 0, `branch_push`, which rebases and pushes. On `conflict: true`, edit the listed files, `commit`, and `branch_push` again (the `thulu-github` manual has the steps) |

- A conflict in a locked test: keep the locked test's assertions and fit
  your code to them. If the target changed what the test asserts, stop with
  `verdict: block`, `reason: spec_test_conflict`, naming the test and the
  target's commit.
- After a rebase, your base is `git merge-base HEAD origin/<default>`, not the
  brief's **Base commit**, for every command of this run that takes `<base>`.
- Rebase does not replace the fix a send-back asked for: do both.

## Verdict

Always leave `verdict.md` in the output dir (the full path the prompt gives
you) and exit cleanly: Thulu reads it only after a clean exit, and only from
that file; a verdict in `_index.md` or in a comment does not count. Without it, or with one whose front matter does not parse, the
task is blocked in Development.

```
---
verdict: continue | block
reason: spec_test_conflict | nothing_to_change | other   # only with block
---
3 to 15 lines the people read.
```

| Situation | Front matter |
|---|---|
| done, everything green | `verdict: continue` |
| a locked test and the spec demand different things, or a test is impossible | `verdict: block` and `reason: spec_test_conflict` |
| the behavior already exists; you leave no diff | `verdict: block` and `reason: nothing_to_change` |
| over the size budget, two failed plans, an environment you cannot fix | `verdict: block` and `reason: other` |

With `block`, add `ask` to the front matter: what you need from the person,
in one or two sentences (skill `octopus-run`). The body, 3 to 15 lines, is
what the people read in Reports. For `continue`: the locked tests and the
suite with their commands and results. For a conflict: the
test as `path:line`, the criterion, and what each demands. For nothing to
change: the output that shows the behavior already works.

After a send-back, add `delta` to the front matter (skill `octopus-run`):
each finding the send-back named, by its ID, as fixed or still open. Add
`next` when Verification has something specific to re-check. The next
stages read both in their brief.

## What you leave

- The code and its commits.
- `verdict.md`.
- `_index.md`, with these sections: Start (HEAD), Locked tests before (each
  failure message), Plan, Attempts (reverted, and what you learned), Criteria
  (criterion, files changed, test that covers it), Commands (each with its exit
  code), Outside the surface (each file with its justification), Diff size
  (production lines added and deleted, and files), Not done (anything the spec asks that you
  could not do, and why). The auditor sees the task, this report and the repo,
  and cannot run anything. After a resume (answers or requested changes) the
  output dir starts empty: write `_index.md` whole again, with the start
  commit of the run you resumed.

## Skipped columns

The brief lists the columns this task skips. When your change makes one of
them necessary, remove its skip with `task_skip_remove` and a reason that
names the files, before you write `verdict.md`:

- `testing` is skipped and you touched a component, page, template,
  stylesheet, locale string or anything else a person sees.
- `readiness` is skipped and you added a migration, a permission check, a
  secret or a new dependency.
- `test_design` was skipped and the code turned out to need behavior a
  locked test should pin. The task goes back to test design when your run
  ends cleanly.

Write each removal in `_index.md`. You cannot add a skip.

## When you stop

- `spec_test_conflict` or `nothing_to_change`: write `verdict.md` and finish.
  Do not edit the test or work around it.
- Never stop for what is not this change's: a failure or hang that also
  happens on base (thulu-gates marks it pre-existing or quarantined), a
  check reported `accepted`, a check that ended unverified (Verification
  decides on it, reusing your run), or a format or lint failure in a locked
  test file (Verification sends it to test design). Name each in the report
  and in `next`, and `continue`. A failure your change causes is yours to
  fix, not a reason to stop.
- A decision the spec leaves open that the tests do not settle: one
  `task_ask`, blocking, before the expensive work.
- The environment fails twice: report the exact error, `block` with
  reason `other`.

## What you do not do

- Touch the locked tests, their fixtures or the test plan.
- Add `//nolint`, `eslint-disable`, `@ts-ignore`, `# type: ignore`, `# noqa`
  or skip markers.
- Fix things you noticed on the way that the spec does not ask for: list them
  in the report.
