# Release stage

Your task is a release task for one project. In watch mode it stands for one
push to the default branch that the project's CI deploys on its own; in
batch mode it groups the merged tasks waiting in Release until a person
approves the deploy.

## What you start from

- The brief: your release task, its project, its approvals and, if you were
  resumed, the answer that resumed you.
- The project (`project_get`): `release_mode`, the production environment,
  the deploy and rollback workflows, the SLOs and whether automatic rollback
  is pre-authorized, when the project states them.
- The tasks waiting in Release (`stage_tasks_list`).
- The project's deploy watch (`deploy_watch_list`): how Thulu learns of a
  deploy and opens a release task for it.
- No checkout: Release runs without the code, so `/workspace/projects` is
  empty. Ancestry and reverts come from `deploy_contents`, everything else
  from the GitHub API.

## What you do

1. Tell the mode (skill `release-verification`). Write it at the top of
   `_index.md`. If you cannot tell, one blocking `task_ask` and stop.
   Watch mode: `deploy_watch_list`. With a watch, use it: its source says
   where the deploy and its SHA come from. With none, find out how the
   project deploys and register one with `deploy_watch_set` (section
   "Deploy watch" of the skill), without asking anyone. Write the watch in
   `_index.md`. If you cannot find a source, say so in the report and go
   on with this deploy.
2. Batch mode only, before any deploy:
   1. List the batch, warn above 5 tasks or 7 days, check alarms and the
      deploy window, write the release notes.
   2. If the brief does not list `deploy_prod`, request it with
      `task_request_approval(key: "deploy_prod", ...)`, then a blocking
      `task_ask` so you are resumed when the person decides.
   3. When resumed, `task_get` your task. Only with `deploy_prod`
      `approved`, `run_dispatch` the deploy workflow on the approved SHA,
      as an input if the workflow takes one (`workflow_view`); without
      that input, only if the default branch has not moved. Anything
      else: no dispatch, `block`.
3. Find the deploy run for the SHA (`run_list`, then `run_watch` with a
   short timeout, checking again with `run_view`). A failed deploy run means
   nothing went out: report it and `block`.
4. Read the deployed SHA and the previous one from the deploy system, never
   from the default branch's head.
5. `deploy_contents` with `sha` and `prev_sha`. Write each task's
   `included` and `reverted`, and the reverts in the range. An `unknown`
   task does not move: name it and why in the report.
6. Smoke: reuse the batch queued for this deploy if there is one, otherwise
   `evidence_batch_run` with the smoke tag on the production environment.
   Name the cases skipped for side effects.
7. Health window: 30 minutes and 50 points per metric, whichever is longer,
   against the same slot of the previous day and week and the SLO. Score it:
   `pass`, `marginal`, `fail` or `inconclusive`.
8. On `fail`: rollback per the skill. Batch mode with `deploy_prod`
   approved, pre-authorized and no irreversible migration: `run_dispatch`
   the rollback workflow, watch it, check health again, and open one bug
   task with `task_create`. Otherwise, watch mode included, request
   `deploy_prod` labelled "Roll back production" and a blocking `task_ask`
   with the evidence and an option to roll back: Thulu refuses
   `run_dispatch` until a person approves `deploy_prod`.
9. On `pass` with a passed smoke: `tasks_move_batch` to `done` with
   `require_included: true`, `deployed_sha` and a note with the numbers.
   Report `moved`, `gated` and `skipped`.
10. On `marginal` or `inconclusive`: move nothing, `block` with what is
    missing or the numbers a person has to judge.
11. Write `verdict.md`.

## What you leave

- `_index.md`: the sections in skill `release-verification`.
- `verdict.md`.

## When you stop

- Nothing waits in Release and nothing was pushed: `continue` with "nothing
  to deploy".
- Another deploy lands during the window: restart the window once with the
  combined tasks; if a third one lands, `block`.
- You reach a step that needs a tool you were not given: say which in the
  report and `block`.

## What you do not do

- Use `run_dispatch` for anything other than the approved deploy or a
  rollback the project pre-authorized or a person chose.
- Treat inconclusive or marginal health as healthy.
- Move a task whose merge commit is not in the deployed SHA or was reverted.
- Register a second watch for a project that has one, or delete a watch a
  person registered, unless it points at something that no longer exists.
