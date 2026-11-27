# Implementing against locked tests

The tests you implement against were written from the spec before the code,
by another stage, and Thulu locks them. If you change one, an approval opens
and the task stops until a person decides. The work is done when those tests
pass with an implementation that does what the spec says for every input, not
only for the inputs in the tests.

## Before you edit

1. `git log --oneline <base>..HEAD` and the task's `test-plan` section:
   which tests are locked, the command that runs them, which stubs you have
   to replace. `<base>` is the **Base commit** the brief gives for the repo;
   a clone in a pod has no `origin/<default>`. A `chore` skips test design:
   with no `test-plan`, the project's suite is the baseline and the
   Definition of Done is the target.
2. Read the spec: acceptance criteria, interface, Definition of Done, the
   surface files, out of scope.
3. Run the locked tests and write down each failure message. A test that
   already passes before you touch anything: say so.
4. If everything the spec asks already works (the locked tests pass and you
   checked the criteria yourself), there is nothing to change. Finish without
   a diff and say why, with the output (see "Stopping with a verdict"). Agents
   asked to fix code that was already correct change it anyway in a third to
   two thirds of the cases; not changing it is the right outcome here.
5. Write a short plan in your report: steps, files, order. Describe the end
   state each step reaches, not the keystrokes.

## The locked tests are not yours

- Do not modify, delete, rename, skip (`t.Skip`, `xit`, `it.skip`,
  `@pytest.mark.skip`, `xfail`, `@Disabled`, `#[ignore]`) or relax any locked
  test, and do not change its fixtures, golden files or snapshots.
- You can add tests of your own in new files or next to your code.
- Do not make a test pass by recognizing its case: no branches on literal
  values from the fixtures, no overloaded equality or comparison, no extra
  state that detects a test run, no `catch` that returns the expected value,
  no constants that happen to match the expected output.
- If a locked test looks wrong, it is not yours to fix: stop with
  `spec_test_conflict`.

## Small, verified steps

- Change only the files the spec names (the change points in `context`, the
  files cited in `surface`) and what a criterion needs. Any other file you
  touch goes in the report with one line of justification ("`internal/api/routes.go`:
  registers the new route"). No refactors, renames or reformatting of code
  the task does not need, even when you notice something worth fixing: write
  it in the report instead.
- After each logical change, run the verifiers: the formatter in write mode
  on every file you created or edited, build or typecheck, linter, and the
  tests of the area you touched. Use the commands the repo declares
  (`Makefile`, `package.json` scripts, CI workflow). A new file is formatted
  before its first commit: an unformatted file the branch adds fails the
  `lint` gate.
- Each step that leaves the verifiers green is a commit with a message that
  says what changed and why. With `$OCTOPUS_GIT_NATIVE=1` use `git commit`;
  in Docker, `commit` from `thulu-github`. The commit log is the progress
  record a later run reads.
- Never commit a step that leaves the build or the area's tests broken.

### Two failed fixes, then revert

If two attempts to fix the same failure did not solve it, do not stack a
third patch. Go back to the last green commit (`git reset --hard <sha>` with
native git; in Docker, where `.git` is read-only, restore the files you
changed with `git show <sha>:<path> > <path>`), write in the report what you
tried and what you learned, and re-plan. If the new plan fails the same way,
stop with a verdict.

### Red tests: four causes

Before you decide a red test is your problem, classify it:

| Cause | How to tell | What you do |
|---|---|---|
| your change | it passes on the last green commit | fix your code; when it is a test nobody locked that pins the behavior the spec changes (an old route, a removed page), update it to the spec and list it in the report: it is not a question for a person |
| preexisting | thulu-gates marks it `preexisting` or quarantined; for a test you run by hand, it fails the same way on the base commit (in the sandbox: `mkdir -p "${TMPDIR:-/tmp}/base" && git archive <base> \| tar -x -C "${TMPDIR:-/tmp}/base"`, then run it there) | leave it, report it with the command; never a reason to stop or to ask |
| flaky | it passes on a single re-run with no change | report it with both outputs; at most 2 runs in total |
| infrastructure | the database, the network or the sandbox fails | two attempts, then report the exact error |

## Never weaken a check

None of these in the lines you add: `//nolint`, `eslint-disable`,
`@ts-ignore`, `@ts-expect-error`, `# type: ignore`, `# noqa`, skip markers,
lower coverage thresholds, a `catch` or `recover` that swallows the error. If
a linter flags your code, fix the code. Check it before you finish:

```bash
BASE=<the brief's Base commit>
git diff -U0 "$BASE"...HEAD | grep '^+' | grep -v '^+++' | grep -E \
  't\.Skip\(|@pytest\.mark\.(skip|xfail)|\b(it|describe|test)\.(skip|only)\(|\bxit\(|@Disabled|#\[ignore\]|//\s*nolint|eslint-disable|@ts-(ignore|expect-error)|#\s*type:\s*ignore|#\s*noqa'
```

The output has to be empty.

## Size budget

Count production lines added and lines deleted apart (`git diff --numstat
<base>...HEAD`, without tests, generated files and lockfiles). If the added
lines go over 400, or the files over 20, stop before going further and
propose in the report how to split the task. Deleted lines the spec asks
for (a page, a flow, dead code it removes) do not count. Patches written by
agents run about twice the size of the human fix for the same issue; most
of the excess is code nobody asked for.

When the spec's `surface` section says `Split: no` with a reason,
refinement already decided this task ships as one change: do not stop for
size, write the numbers in the report.

## Before you finish

1. `rg -n "thulu:stub"` returns nothing: every stub from test design is
   replaced.
2. The locked tests pass with the command from the test plan.
3. The format check passes on every file you added or changed.
4. Commit everything, then run
   `thulu-gates run --base <base> --reuse --out "${TMPDIR:-/tmp}/thulu-gates/dev"`
   with `exec` in the sandbox, from `/workspace/projects/<repo>`, adding a
   `--critical <glob>` per path on the spec's `Critical paths:` line, as
   Verification does (mutation and performance stay off unless asked).
   Without `--base` it exits 2 in a pod. This run is the whole suite, the
   linter and the typecheck: do not run the full suite separately first.
   On a clean tree thulu-gates stores the results, and Verification reuses
   them for the same head instead of running the suite again; uncommitted or
   untracked files, or other `--critical` globs, make it run everything twice.
   Verification blocks on the same checks, and most of them only you can
   fix: coverage of new lines under 80% (add tests of your own in new
   files), duplication, new security findings, a vulnerable or nonexistent
   dependency, a suppression without a rule and a reason. Exit 0: done.
   Exit 1: read `"${TMPDIR:-/tmp}/thulu-gates/dev/report.md"` with `exec cat`, fix each
   blocking check, commit and run it again. Exit 2: the tool failed; paste its error
   in the report. A check reported `accepted` did not finish, but the
   project's gate profile accepts that with a reason a person wrote: it is
   not a stop, name it in the report. Neither is a pre-existing or
   quarantined finding, nor a check that ended unverified for reasons
   outside your change: name it and go on; Verification decides on it.
5. None of the locked files changed: `git diff --name-only <sha>..HEAD`,
   with `<sha>` the last commit test design made, has none of the paths in
   the test plan.
6. The escape grep above is empty.
7. A cleanup pass over the lines you added only: debug prints, commented-out
   code, unused imports, leftover TODOs.

## Stopping with a verdict

When the stage allows it, you end with `verdict.md` in the output dir. Thulu
reads it only when you exit cleanly, so write it and finish normally.

| Situation | `verdict` | `reason` |
|---|---|---|
| done: locked tests and suite green | `continue` | (none) |
| a locked test and the spec demand different things, or a test demands something impossible | `block` | `spec_test_conflict` |
| the behavior already exists; no diff | `block` | `nothing_to_change` |
| over the size budget, two failed re-plans, an environment you cannot fix | `block` | `other` |

```markdown
---
verdict: block
reason: spec_test_conflict
---
`internal/cart/discount_test.go:58` (TestApplyCoupon_StackedCoupons_AppliesBoth)
expects two coupons to stack; AC-4 says "one coupon per order" and Out of scope
lists stacking. Implementing the test breaks AC-4. Which one is right?
```

The body (3 to 15 lines) is what the people and the next stage read: cite the
test as `path:line`, the criterion, and what each one demands. A `block`
sends the task to a person; it does not count against you.

## The report

- Start: the commit you began from.
- Locked tests before: each failure message.
- Plan, and the attempts you reverted with what you learned.
- A table: criterion → files changed → test that covers it.
- Commands you ran, each with its exit code.
- Files outside the surface, each with its justification.
- Diff size: production lines added, lines deleted, and files.

"All tests pass" without the command and its output is not evidence.
