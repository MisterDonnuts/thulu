# In progress

You implement the task on its branch and leave it ready for a person to
review. `octopus-sandbox` covers installing, building and running the
project.

## Before you edit

1. Read the brief: the title, the description, the Definition of Done when
   there is one, and the discussion. A person may have narrowed the scope in
   a comment.
2. If the task came back from Review, the brief and the discussion say what
   the person wants changed. Do that and nothing else; your earlier commits
   are on the branch.
3. Find the code the task touches and the project's own commands for
   formatting, lint, typecheck and tests: the README, CONTRIBUTING, the
   Makefile, `package.json` or the CI workflow name them. Run the tests of
   that area once before editing and note what already fails.
4. If the task does not say what has to change, ask one blocking question
   with `task_ask` before the expensive work instead of guessing. If what it
   asks for already works, stop with `nothing_to_change`.

## The change

5. Make the change, following the conventions the repository already uses.
   Keep to what the task asks; list anything else you notice in the report.
6. Add or update tests for the behavior you changed.
7. Format every file you created or edited with the project's formatter
   (write mode, on those files only), then run the format check, the linter,
   the typecheck and the tests for what you touched. Never leave a new file
   unformatted. A failure your change causes is yours to fix. A failure that
   was there before you started goes in the report with its output; it is
   not a reason to block.
8. Commit each green step with a message that says what changed and why.
   With `$OCTOPUS_GIT_NATIVE` set to `1`, use `git commit`; otherwise use
   `commit` from `thulu-github`. Do not push, rebase onto another branch or
   open a pull request.
9. Stop every process you started: dev servers, watchers, databases,
   background jobs.
10. Check the Definition of Done items you can prove with
    `task_checklist_check`, with the evidence in the `note`.
11. Write `_index.md` and `verdict.md`.

## Verdict

Always leave `verdict.md` in the output dir (the full path the prompt gives
you) and exit cleanly. Thulu reads it only after a clean exit, and only from
that file: a verdict in `_index.md` or in a comment does not count. Without
it, or with front matter that does not parse, the task stays blocked in In
progress.

```
---
verdict: continue | block
reason: nothing_to_change | other   # only with block
ask: "What you need from the person, in one or two sentences."   # only with block
---
3 to 15 lines the people read.
```

| Situation | Front matter |
|---|---|
| the change is committed and the checks you ran are green | `verdict: continue` |
| the behavior already exists; you leave no diff | `verdict: block` and `reason: nothing_to_change` |
| still unclear after asking, a failure you cannot fix, an environment that fails twice | `verdict: block` and `reason: other` |

With `continue` the task moves to Review. The body lists the commits, each
check with its command and result, and what a reviewer should look at first.
With `block`, `ask` says what the person has to decide or bring (skill
`octopus-run`).

## What you leave

- The code and its commits.
- `verdict.md`.
- `_index.md`, with: what the task asked, the files you changed and why, the
  commands you ran with their exit codes, the failures that were there before
  you started, and what you did not do and why.
