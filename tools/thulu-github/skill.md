# Rules before anything else

**First check how you write to git**: `echo $OCTOPUS_GIT_NATIVE`.

## With `OCTOPUS_GIT_NATIVE=1` (Thulu on Kubernetes): plain git

Your `/workspace/projects/<repo>/` is a real clone whose `origin` is Thulu, not
GitHub. Use git from the terminal as in any repo:

```bash
git add -A && git commit -m "<clear message>"
git fetch origin && git rebase origin/<default>   # if main moved ahead
git push                                          # no arguments
```

- `git push` leaves your branch in Thulu, which publishes it to GitHub as the
  task's branch before handing control back to you. When it finishes, the branch
  is already on GitHub and you can call `pr_create`. The GitHub token never
  passes through your container.
- If the rebase conflicts, you resolve the conflicts yourself, here: edit the
  files, `git add`, `git rebase --continue`, and `git push`. After a rebase
  `git push` rewrites the branch without you doing anything else.
- `origin/<default>` is what GitHub has: Thulu updates it every time you run
  `git fetch`.
- Don't name another branch in the push (`git push origin other`): Thulu
  rejects it.
- If the push to GitHub fails, `git push` still succeeds and Thulu leaves a
  comment on the task with the error. `branch_status` tells you whether the
  GitHub branch is up to date.

The MCP's `commit` and `branch_push` tools are no use to you in this mode:
`commit` tells you to commit in the terminal and `branch_push` only publishes
what you already pushed.

## Without `OCTOPUS_GIT_NATIVE` (Thulu on Docker): git through the MCP

Your worktree's `.git` is mounted read-only: `git commit` fails and your
container has no credentials to push.

| Not from Bash | With the MCP |
|---------------|------------|
| `git add` + `git commit` | `mcp__thulu-github__commit { "message": "..." }` |
| `git push` | `mcp__thulu-github__branch_push` |
| `git rebase` / `git merge` / `git pull` | `branch_push` rebases on its own (see below) |

`commit` and `branch_push` don't call the GitHub API: they ask Thulu to run
git where the real `.git` and the key are. They are idempotent: if there is
nothing new, `commit` returns `skipped: "no changes to commit"`.

## In both

- To open the PR: `mcp__thulu-github__pr_create`, never `gh pr create` or curl
  to GitHub.
- Reading always works: `git status`, `git diff`, `git log`, `git blame`.
- When a stage run ends, Thulu commits whatever you left uncommitted, whether
  the run succeeded or failed, and in a stage that pushes it publishes the
  branch. Committing yourself is for splitting the work into commits with your
  own messages, not for getting the work saved.

---

GitHub API behind an MCP. The token never touches you: the server resolves it per project from your `task_id`. All you need to know is **which tools you have** (it depends on the scope you were given) and **how you identify the repo**.

## Scope

Your agent's compose declares the scope (`@read` or `@write`) per stage. If you call a tool outside your scope, the server rejects it with a clear message. Don't make it up.

- **read**: `pr_view`, `pr_diff`, `pr_list`, `pr_files`, `pr_comments_list`, `pr_review_threads`, `pr_reviews_list`, `pr_reviewers`, `pr_checks`, `labels_list`, `workflow_list`, `workflow_view`, `run_list`, `run_view`, `run_watch`, `run_jobs`, `run_logs`, `run_artifacts`, `run_pending_deployments`.
- **write**: everything in read + `pr_comment`, `pr_review_submit`, `pr_review_comment_reply`, `pr_review_thread_resolve`, `pr_review_thread_unresolve`, `pr_reviewers_request`, `pr_reviewers_remove`, `pr_labels_add`, `pr_labels_remove`, `pr_labels_set`, `pr_assignees_add`, `pr_assignees_remove`, `pr_update`, `pr_ready_for_review`, `pr_create`, `run_rerun`, `run_cancel`.
- **outside the ladder**: `run_dispatch`, `deployment_approve` and `pr_merge`. No scope grants them, not even `write`. You only have them if your stage names them one by one. If you don't see them in your tool list, it's not a bug: you weren't given them.

## In a chat

If you are in a chat with no task, the token comes from the workspace project whose repo you name in `repo`. `commit`, `branch_push` and `branch_status` don't work there (there is no worktree). `pr_merge` and `deployment_approve` don't run right away: they wait for the person to confirm them in Thulu and the tool returns `awaiting_confirmation`. Tell them it is waiting for their confirmation and don't call it again.

## Identifying the repo

Every tool takes `repo` as `"owner/name"` (e.g. `acme/web`). If the task has a linked project, the project's remote is the first candidate; read it from the brief.

## Pull Requests

### `pr_view`: always start here

Before asking for the full diff, ask for metadata. It tells you how many files changed, lines, author, mergeable, etc. If the PR changes 200 files, you'll use `pr_files` paginated, not `pr_diff`.

### `pr_diff` vs `pr_files`

- `pr_diff` gives you the full unified diff in one go. Capped at ~80KB. For small PRs.
- `pr_files` gives you an array of files with `{filename, status, additions, deletions, patch}`. Each `patch` is also capped per file. Paginate with `page`/`per_page` (default 30, max 100).

Rule of thumb: if `pr_view` reports `changed_files > 15` or `additions + deletions > 800`, go to `pr_files`.

### `pr_comment` vs `pr_review_submit`

- `pr_comment`: top-level comment (issue-style). For general feedback or links.
- `pr_review_submit`: review with a verdict (`APPROVE`, `REQUEST_CHANGES`, `COMMENT`). Accepts inline `comments[]` tied to `{path, line, body}`. It's what you want when your stage is reviewing the PR.

`APPROVE` fails if you are the PR's author; GitHub doesn't allow self-approval. If that happens, use `COMMENT`.

### Reading a PR's feedback

All of these are `read` scope. Before addressing a review:

1. `pr_review_threads({repo, number, state: "unresolved"})` → the inline threads still to resolve, with `path`, `line` and the `diff_hunk`. The thread's `id` is the one the tools that resolve it take; each comment's numeric `id` is the one the reply tool takes.
2. `pr_checks({repo, number})` → CI on the PR head with a `verdict` (`failing`, `pending`, `passing`, `none`). With `only_failed: true` it lists only what's broken. If `source` says `actions`, the token couldn't read check runs and the CI came from GitHub Actions runs; each run's `id` goes to `run_jobs` and `run_logs`.
3. `pr_comments_list({repo, number})` → the PR's general conversation. With `since` you read only what arrived since your last pass.

`pr_reviews_list` returns each submitted review with its verdict and its summary. `pr_reviewers` tells you who still owes a review (`requested_users`, `requested_teams`) and each one's latest verdict. `labels_list` lists the repo's labels, so you pick one that exists before labelling.

The lists read every page up to a cap (300 by default) and return `capped: true` when they stopped there.

### Acting on a review's feedback

All of these come with `write`. The order for each review thread:

1. `pr_review_threads` gives you the threads, each with its `thread_id` and the `comment_id` of its comments.
2. Fix what it asks for and push the fix.
3. Reply inside the thread: `pr_review_comment_reply({repo, number, comment_id, body})`, with what you changed and in which commit, or why you're leaving it as is. Don't use `pr_comment` for this: it ends up loose, far from the line under discussion.
4. Only then `pr_review_thread_resolve({repo, thread_id})`.

Never resolve a thread you didn't address. If you disagree with the request, reply in the thread and leave it open: whoever opened it closes it. `pr_review_thread_unresolve` reopens one resolved by mistake.

PR metadata:

- `pr_labels_add` / `pr_labels_remove` / `pr_labels_set`: only labels that already exist in the repo (`labels_list`). An unknown name is rejected with the closest matches and nothing changes; labels can't be created from here.
- `pr_reviewers_request` / `pr_reviewers_remove`: logins in `reviewers`, team slugs in `team_reviewers`; at least one. GitHub doesn't allow requesting a review from the PR's author.
- `pr_assignees_add` / `pr_assignees_remove`: the logins GitHub couldn't assign come back in `not_assigned`.
- `pr_update`: title, body or base. The body is replaced whole: read it with `pr_view` if you want to add to it. It doesn't close PRs.
- `pr_ready_for_review`: takes the PR out of draft when it's ready for review; `draft: true` turns it back into a draft.

### `pr_create`

**It doesn't push code.** Before you call it the branch has to be on GitHub: with `OCTOPUS_GIT_NATIVE=1`, `git push` from the terminal; without it, `branch_push` (below). If you pass a `head` that doesn't exist on the remote, GitHub responds 422.

### `branch_push`

This section is for Docker (without `OCTOPUS_GIT_NATIVE`). With `OCTOPUS_GIT_NATIVE=1` it only publishes what you already pushed with `git push` and never rebases: you do the rebase yourself in your clone.

Asks Thulu to push the task's branch to `origin`. No args (the task is resolved from the SSE). Idempotent: if it's already pushed, it returns `skipped: "no commits ahead of base"`.

**Auto-rebase.** Before pushing, Thulu runs `git fetch origin <default>` and, if your branch is behind, rebases onto `origin/<default>` and pushes with `--force-with-lease`. This keeps the PR from ending up non-mergeable when `main` moves ahead during the task. The response carries `rebased: true` when that happened.

**Rebase conflicts.** If the rebase hits conflicts, Thulu aborts (leaves the worktree clean, with no `<<<<<<<` markers in files) and returns:

```json
{
  "conflict": true,
  "conflict_base": "origin/main",
  "conflict_files": ["path/a.go", "path/b.go"],
  "conflict_action": "Edit the listed files…"
}
```

To resolve them:
1. For each file in `conflict_files`, read it, understand what changed in `main` that clashes with your change, and edit the file so it reflects **the correct merge** (your change applied on top of main's new state).
2. Call `commit` with a message like `"resolve rebase conflicts with origin/<default>"`.
3. Call `branch_push` again; Thulu retries the rebase from scratch. If conflicts remain, repeat.

**On Docker don't try** running `git rebase`, `git merge` or `git pull` from Bash: your container can't reach `origin` with credentials. All coordination with `origin` goes through the MCP.

### `branch_status`

Reports `ahead/behind/up_to_date/needs_rebase` for your branch against `origin/<default>`. No args. Useful when you want to decide whether to commit more, or to confirm that an earlier rebase landed clean. It does **not** trigger a push or a rebase; it's read-only.

**The flow for opening the task's PR:**

```
1. You edit files in /workspace/projects/<repo>/.
2. You commit per conceptual chunk (optional: if you skip it, Thulu commits
   everything when the run ends).
   - OCTOPUS_GIT_NATIVE=1: git commit in the terminal
   - Docker: mcp__thulu-github__commit { "message": "<clear message>" }
3. You publish the branch.
   - OCTOPUS_GIT_NATIVE=1: git push (rebase first if main moved ahead)
   - Docker: mcp__thulu-github__branch_push  (no args)
4. mcp__thulu-github__pr_create with a title and body you wrote:
   - body with: what changed, why, how it was tested, link to the sandbox preview
   - draft=true if it's not ready for review yet
5. Name the PR in your report (_index.md). No need to register it: Thulu
   links it to the task as soon as pr_create opens it.
```

A subtask doesn't open its own PR: its work travels in the root task's PR.

If nobody opens the PR, a person has the **Force PR** button in the task's
menu, and Thulu opens it on its own when the task enters a stage marked
"Opens PR" in the board settings. That generic PR doesn't say what changed or
why: you open the good one, since you have the context.

## GitHub Actions

### Finding runs

`workflow_list` → `run_list` filtering by `workflow_id` (numeric or a filename like `'ci.yml'`) and optionally `branch` and `status`. The usual case: you want the latest run on the PR's branch.

### Reading logs

`run_logs` with only `run_id` gets you the run's full zip, which **is not unpacked server-side**: the server detects it and fails, asking you for `job_id`. For a plain-text log always pass `job_id`, and the `job_id` comes from `run_jobs`.

Typical workflow when a check fails:
1. `run_list({repo, workflow_id, branch, status: "failure"})` → take the latest run.
2. `run_jobs({repo, run_id, failed_only: true})` → the broken jobs, with the step breakdown. Often the name of the failed step already tells you what happened and saves you step 3.
3. `run_logs({repo, run_id, job_id})` for the text of the job you care about.

`run_jobs` returns by default the jobs of the run's **latest attempt**. If there was a `run_rerun` and you also want to see the earlier attempts, pass `filter: "all"` and look at `run_attempt`.

`run_artifacts({repo, run_id})` lists what the run uploaded (coverage, binaries, reports). The `archive_download_url` needs a token, so it's for pasting into your output for a human to open; the server doesn't download or unpack artifacts.

### Following a run to the end

`run_view({repo, run_id})` is the cheap read of a run you already know: status, conclusion, attempt, timings.

`run_watch({repo, run_id})` is the one you want when you're waiting for a result. It polls server-side until the run finishes, with three outcomes:

- finished → `conclusion` comes back, and if it isn't `success` it already carries `failed_jobs` with the `job_id` and the step that broke. The next step is `run_logs`, not `run_jobs`.
- `status: "waiting"` → the run hit a protected environment and is waiting for a human to approve it on GitHub. `run_watch` stops there on purpose (with `pending_deployments` inside) because waiting for a person inside a tool call does nothing for you.
- `timed_out: true` → the wait budget ran out and the run is still going. Call again.

Default 120s of waiting, polling every 10s. Raise it with `timeout_seconds` (cap 600) if the pipeline is long, but remember there is a tool call timeout on the other side.

### Triggering workflows

Before triggering, `workflow_view({repo, workflow_id})` gives you the YAML: there you see whether it accepts `workflow_dispatch` and **which inputs it declares**. Blindly triggering a workflow that doesn't declare them fails with 422.

`run_dispatch` returns the run it created (`run_id`), not a bare 204: the server finds the new id by comparing against the runs that existed before triggering. If GitHub takes more than 20s to queue it, it returns `run_id: null` and tells you how to find it.

`inputs` must be strings: if the workflow declares `version: number`, send `inputs: { version: "3" }`, not `{ version: 3 }`.

`ref` is a branch or tag. It accepts `main` or `refs/heads/main`.

### Deploy: the full path

```
1. workflow_view({repo, workflow_id: "deploy.yml"})   ← accepts dispatch? which inputs?
2. run_dispatch({repo, workflow_id: "deploy.yml", ref: "main", inputs: {...}})
3. run_watch({repo, run_id})                          ← until completed or waiting
4a. conclusion success  → run_artifacts if the run left evidence; report the html_url
4b. conclusion failure  → run_logs({repo, run_id, job_id}) with the job from failed_jobs
4c. status waiting      → report who has to approve and stop. It's not your decision.
```

If the deploy is going badly wrong, `run_cancel({repo, run_id})` stops the jobs still running. **It's not a rollback**: whatever was already applied stays applied. Say so in your report instead of assuming that cancelling undid something.

### Environment approvals

`run_pending_deployments({repo, run_id})` tells you which environment the run is stopped at, how long it's been waiting, who can approve it and whether this run's token is one of them.

`deployment_approve` exists but you'll almost never have it. An environment with required reviewers is there so a person decides; if you have the tool granted it's because someone decided to automate that review on purpose. When in doubt, report and wait.

### Re-run

`run_rerun({run_id, failed_only: true})` re-runs only the jobs that failed. Use it when the failure was something flaky, not to re-run "just because". Careful re-running a deploy run: it re-deploys.

## Operating rules

1. **A single pass.** Don't request the same PR/run multiple times "to verify". Think about what you need beforehand and fetch it in one go.

2. **404 errors = wrong scope or token.** If a tool fails with 404 on a repo you know exists, your project's token almost certainly lacks access. Report it in your output, don't insist.

3. **422 errors on `pr_create`.** Almost always: branch not pushed, base that doesn't exist, or there's already an open PR from that head. Read the message and pivot.

4. **Don't commit secrets in comment bodies.** If you paste logs into `pr_comment`/`pr_review_submit`, make sure they don't include tokens, env vars, credentials.

5. **PR/comment bodies: markdown.** GitHub renders standard markdown + some extensions (task lists, mentions, `#123` refs). No inline HTML.
