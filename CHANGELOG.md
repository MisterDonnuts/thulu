# Changelog

All notable changes to Thulu are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

## [1.2.0] - 2026-11-27

An eleven-column Thulu flow where agents refine, test, build, verify, review and release a task, and a redesigned task view and chat to follow it.

### Before upgrading

- **Update every image together with the daemon.** `thulu update` and the Helm chart do. An install that builds its own images rebuilds the agent, sandbox, runtime, thulu-browser (2.2.0), thulu-github, thulu-cloud and octopus-api images: an old agent image never uploads `verdict.md` on Kubernetes, so a stage whose agent decides stays blocked with "did not write its verdict", and an old thulu-github fails on projects connected with a personal token.
- **17 migrations** (`auth` 34, `automation` 7, `evidence` 5, `project` 32–34, `stages` 7–8, `task` 51–59). They only add columns and tables, with defaults that leave existing boards as they were: no send-back cap, no skipped columns, no locked tests, no draft PRs until someone sets them.
- **A run's default GitHub token can no longer merge.** It reads contents and writes pull requests but lacks `contents:write`; `pr_merge` asks for it by name and gets it only where the column grants that tool. A column that grants `run_dispatch` gets `actions:write` only after a person approves `deploy_prod` on the task, and `run_rerun` and `run_cancel` wait with it: a custom agent that dispatches deploys has to call `task_request_approval(key: "deploy_prod")` first.
- `POST /tasks/{id}/flags/import` with a run's key answers 403 when the run's stage has no `ingest_flags`.
- A person moving a task back to an earlier column has to send a `reason` (400 `api.tasks.errors.reason_required_send_back` otherwise). API keys and agents are not affected.
- An Evidence take uploaded with an API key or recorded by an agent clears the case's "Safe to run in production"; mark it again after reviewing the new take.
- The agent-to-agent reply cap in a comment thread goes from 3 to 5 and becomes a workspace setting (`agent_turns_per_thread`, 1 to 20).
- **Runners.** The first update from 1.1.0 runs the old CLI, which doesn't replace the `thulu` on your `PATH`: if `thulu version` still shows the old version, run `thulu update` again or reinstall with `install.sh`. Then `thulu runner service install` points the service at the new CLI, keeps what you added to the service file, and picks up the new shutdown timeouts (60 s in launchd and systemd). A runner with `local_mcp_config` in `runner.json` keeps loading those MCP servers as `local_tools: "selected"`.
- Workshop agents you already installed keep their prompts until you update them from the Workshop; until then the `comment.md` they write on a stage run is not posted.
- Kubernetes: the cleanup sweep deletes `<pod>-env` Secrets left without a pod and needs `list` on Secrets in the `tenant-secrets` role. With an older chart it logs one Forbidden line and leaves them.

### Thulu flow

- **A board where each column has an agent with one job.** Workshop → Flows installs the Thulu flow in one step: Backlog, Refinement, Test design, Development, Verification, AI review, Agentic testing, Production readiness, Human review, Release and Done, with eight agents, nine skills, the column flags, approvals and task types. Agents are written before the board, so the board never names an agent that isn't installed. Settings → Boards → "Install a flow" opens the same tab, and the board card shows the flow and its revision.
- What each agent does: the refiner writes the spec, its acceptance criteria and the task's surface, risk and size; the test designer writes the tests, which get locked; development rebases, implements and commits; verification runs `thulu-gates` against the branch's base; AI review reads the diff; agentic testing exercises what a person would see in a browser and records evidence; production readiness checks that the branch is current and merges cleanly, commits follow the project's rules, no debug files or secrets are left, every case has a passing run with evidence, and earlier reports agree; release watches or dispatches the deploy.
- **Task types.** `feature` goes through every column and asks for "Definition ready" when it leaves Refinement; `bug` too, and the test designer writes the test that reproduces it; `chore` skips Test design and Agentic testing; `release` goes from Backlog to Release.
- **Updates you can review.** A new revision of the flow is offered as an update when you didn't edit the board. If you did (flags, agents, approvals or types; names and colors don't count), the plan says "Board edited" and lets you keep it, replace it or install a copy. An update that would delete columns holding tasks, or leave tasks in a column their type skips, waits until you move them.
- **`thulu init` asks which board the workspace starts with**: the Thulu flow, or a minimal start with Backlog, In progress, Review and Done, the general agent `thulu-agent` in In progress (it implements on the task branch, runs the project's tests and lint on what it touched, commits and leaves a verdict, without pushing) and Review for a person. Either is installed as a board of its own; the workspace's existing board keeps its tasks. A rerun suggests "Leave the boards as they are" and never rewrites an existing board.
- **Agents per column and per task type.** Each column names its own agent, and each task type can name another per column. A new task takes, from lowest to highest priority: the column's agent, its project template's, its type's, and the one its creator names. `thulu export` and `import` carry both and the flow a board came from.
- **Auditors only where they pay.** In the flow only Development and Agentic testing keep an auditor, both after every run. The testing auditor doesn't test again: it checks that every criterion has a case that ran, that passes have evidence you can open, the before and after of each screen touched, and that the verdict matches what was found.
- **Per-task column skips.** The refinement agent can skip later columns a change doesn't need (`skip:` in `flags.yaml`, each with a reason), and a person can add one from the task's Properties. Any agent working on the task, or a person, removes a skip with a reason; if the task already passed that column it goes back there without counting against the send-back cap. The first and last columns, virtual ones, refinement, columns that ask for an approval, PR and release columns, and columns with the new "Tasks can't skip this column" toggle are never skipped. AI review and Human review have it in the flow. A UI change clears the skip of columns that expect `ui`. The task's steps show skipped columns crossed out with who skipped them and why, and the card counts the columns ahead that it skips. API: `POST /tasks/{id}/stage-skips` and `/stage-skips/{stage}/remove`; octopus-api: `task_skip_add` and `task_skip_remove`.
- Agents hand off through `verdict.md` and the brief, not through comments. The brief gets a Handoff section with the send-back that returned the task, the stage's last `kick_back` or `block`, and the optional `next` and `delta` fields of earlier verdicts, plus the latest report of each earlier verdict stage. A stage run no longer posts `comment.md`; agents comment only to talk to a person, and system notes no longer fill the brief's Discussion.
- Prompts stop asking a person what a rule decides: what also fails on base, quarantined tests and checks the project profile accepts don't stop Development, Verification or Readiness; a non-blocking question applies its default in the same run; agents look for the answer in the discussion and the description before asking. The board header counts the agent columns as "In progress" and Human review as "To review".

### Task view

- **A header that says what the task needs.** Crumbs, the task key over the title, the columns the task goes through (approvals ahead as locks, skipped columns crossed out) and one status line. When the task waits for a person it shows "Needs you · N" with the daemon's sentence and one action per kind: See what is missing, Skip cases…, Review the approval, Open the stop. While an agent works it shows the stage, the agent and how long ago it started, with Stop and Cancel. Scrolled down, the header folds into a bar. The ⋯ menu has Re-run, Send back and Move, each with a reason; sending back always asks for one.
- **Tabs**: Overview, Tests, Activity, Files, Chat, Reports and Approvals, with counts. The brief shows the goal open and every other section as a row that opens.
- **Comments with types.** The composer sits on top, and "Waiting for you" pins the open stop, the approvals that hold the stage the task is in and the questions that stage asked. Filters All, Agent questions, Decisions and Conversation with counts, newest or oldest first (saved per person). Decisions carry a tag with their action ("Sent back to Development", "Skipped TC-3"), a stop shows the agent's ask with its report folded underneath, a pause between agents cuts the thread, long bodies fold, and agent questions are cards that open in place to answer. System notes and run cards left Comments.
- **A Tests tab.** "X of N pass · M of K acceptance criteria proven", a square per criterion that filters the list, and "Read the criteria" with the cases covering each. "To finish testing, a person needs" groups the cases the agent couldn't run by what they need, with actions: give the agent access through a secrets form, test by hand with checkable steps, skip with a reason, re-run, or watch the video. The list groups cases into need a person, running again, failing, passing, skipped and not run, with a column per recent testing round. A case shows how to test it (preconditions, steps, pass condition) beside what the agent saw (observed text, video with its `.vtt` track, captures, history by round), and a person can set their own result.
- **Waived is its own state.** A person can skip test cases with a reason (`POST /tasks/{id}/test-cases/waive`); they count apart and never as passes, and Production readiness lists them as known gaps. A run key gets 403. When the skip clears the last case that needed a person, the task moves on and the next agent starts.
- **Give the agent access.** `POST /tasks/{id}/test-cases/access` stores secrets in the project's Evidence environment, queues the cases, posts the decision with the secret names only and relaunches the stage. Only a run of that task in that column can read those secrets. Queued cases return to their last verdict if the relaunched run doesn't run them.
- **Activity** draws the task's journey across the columns (visits per column, the current one marked, skips as dashed arcs, send-backs as arcs underneath, by agent or by person) and one card per round. Each visit says how it ended, links to the reason in Comments and opens to its runs, questions, stops, relaunches and accesses. Filters by column and agent; the last three rounds, with earlier ones on demand; costs; the full event log folded behind "Show all events".
- **Typed comments and decisions in the API.** Comments have a kind (`comment`, `question`, `block`, `decision`, `approval`, `pause`) and an optional `meta`. `PATCH /tasks/{id}` and `POST /tasks/{id}/relaunch` take a `reason` that becomes a decision comment, answering the open stop. `verdict.md` takes `headline`, `ask`, `needs_person` (case, what to do and why: real account, writes real data, device, decision, credential), `delta` and `next`; only `verdict` is required. An agent's block posts a `block` comment, and approvals open their own thread.
- `GET /tasks/{id}` adds `attention` (what the task waits for from a person, with headline, stage, run, items and actions; `null` while it runs), `upcoming_approvals`, `waiting` and `block` (who stopped the task and why). Events and case runs carry the task's `round`.
- **Acceptance criteria**: `GET/PUT /tasks/{id}/criteria` and `POST /tasks/{id}/criteria/approve` (a signed-in person only). A changed criterion loses its approval. Test cases gain `covers` and `preconditions`; runs gain `needs`, `tried`, `tried_result` and `writes_data`, with `needs.kind` from `GET /test-cases/need-kinds`, and `observed` holds at most 280 characters.

### Chat

- **One conversation component** for the side panel and the task's Chat tab. A running turn shows its steps live; `#` names a task, `@` picks an agent (another agent opens a new chat with a summary), `/` runs a command. While a turn runs, Enter queues the message and Cmd+Enter stops the turn and sends.
- The panel is 400 px wide and expands to a page, with the task chip and "Act for me" on top. The sidebar has New chat (Cmd+J), search, and rows grouped by day with their state. In a task, a bar switches between the current run, earlier runs, your chats about the task and a new one.
- Each line the engine prints reaches viewers live and lands in a per-turn transcript (`GET /chats/{id}/turns/{turn}/transcript`). An answer records model, tokens, context size, cost and what its octopus-api calls changed. `POST /chats/{id}/messages` takes `mode` (`queue` or `steer`), `refs` and `handoff_from`; `POST /chats/{id}/turn/cancel` stops a turn, `DELETE /chats/{id}/queue/{id}` takes a queued message back and `PATCH /chats/{id}` links, changes or removes the task. A chat is named after its first message.

### Stage engine

- **The agent decides where the task goes** (`agent_verdict`): it ends with a `verdict.md` saying `continue`, `kick_back` (with the stage and the reason) or `block`. A run that exits 0 without a readable verdict blocks the task naming the agent; a verdict written only in the report doesn't count.
- **A cap on send-backs per task** (`max_kick_backs`, 0 to 20; 2 in the Thulu flow) counts the auditor's, a verdict's and "Request changes". Past it the task blocks where it wanted to go back. A second send-back for the same single-round reason (stale branch, commits off the rules, forbidden files, evidence missing…) also blocks. The auditor can send a run back to its own stage.
- **Conditional columns** (`skip_unless`) run only for tasks whose declared surface matches; a column that expects `ui` still reads the branch diff before skipping. **Locked tests** (`lock_tests`): tests a column writes can't change later without the "Change to locked tests" approval, also on projects that live on a runner. **Draft PRs** (`pr_draft`, `pr_ready`), **a column merged tasks land in** (`merge_target`, with `merge_sha`) and **freeing a task's resources** (`release_resources`).
- An agent's approval can hold a later stage instead of the current one (`stage` in `POST /tasks/{id}/approvals`), and a column's approval can't be approved while that column's agent still runs.
- A non-blocking question closes with its default when the task leaves the stage that asked it. A blocked task says who stopped it and why: the agent and its cause ("gates fail"), the auditor, or the send-back cap.
- **Auditor modes.** Each agent chooses when its auditor runs: after every run (`always`) or only on signals (`signals`): a non-zero exit, a verdict other than continue, locked tests changed, gate config or suppressions in the diff, a diff over budget (400 production lines and 20 files by default), skips the run's agent set, flags raised, no report, the first run after a person relaunched with a reason, or high risk. Otherwise the Activity records "Audit skipped" with every check. API: `auditor_mode`, `auditor_max_diff_lines` and `auditor_max_diff_files` in `/types/{name}/settings`.
- `PUT /types/{name}/settings` merges the body over what is stored; before, a body with only `mcp_by_stage` reset turns, skills, auditor, description and effort to defaults.
- `flags.yaml` reads a bare string as a custom flag instead of refusing the whole file.

### Release, merges and deploys

- **Release mode and production environment** in project settings: Auto (a deploy per merge) or Batch (a release task dispatches what accumulated). `GET /projects/{id}/deploy-contents` says which tasks a deployed commit carries; `POST /tasks/batch-move` moves up to 50 tasks forward through each stage's approvals.
- **Deploy watches.** The release agent registers one when the project has none: a GitHub workflow or environment, a Cloud Run service or a URL that answers with the version. Thulu turns each into a managed automation that opens a release task on every deploy (GitHub by webhook, Cloud Run and URLs polled every 5 minutes to 24 hours, 15 minutes by default). Project settings list them with their last reading.
- **Merge with required checks.** An approval with `required_checks` (the flow's Merge) can be approved while the PR's checks run; the task stays put until they pass, then advances once. A new push returns a decided approval to pending. `POST /tasks/{id}/merge` to the default branch answers 409 while checks aren't green.
- Automations gain a `move` step and the `github.deployment_status` and `github.workflow_run` triggers; GitHub events carry `head_sha`, `before`, `default_branch` and, on a PR, `merged` and `merge_sha`. Evidence cases have "Safe to run in production", and an agent's batch against production runs only those.
- octopus-api adds `stage_tasks_list`, `deploy_contents`, `tasks_move_batch`, `evidence_batch_run`, `evidence_batch_get` and `deploy_watch_set`/`list`/`delete`.

### Agents read more of a task

- octopus-api adds `task_reports` (the Reports tab with each verdict), `task_events` (stage moves, runs with exit codes, verdicts, auditor, relaunches) and `task_attachment_read` (images as images, text, logs and SARIF as text), available to any agent with `octopus-api@read`. Also `task_criteria_get`/`set`, `reply_to` on `task_add_comment`, `reason` on `task_relaunch`, and the new case fields.

### thulu-gates

- **A quality-gate CLI** in the sandbox and runtime images and inside the `thulu` binary that checks only what the branch changes against its base: tests, coverage of new lines, duplication, lint, typecheck, semgrep, secrets, vulnerable or nonexistent dependencies, licenses, new suppressions, and optionally mutation testing and benchmarks. It writes `report.json`, `report.md` and SARIF. A check without its tool is `skip` with the install command, never a pass, and `.thulu/gates.json` is read from the base commit so a branch can't loosen its own gates.
- It blocks only what the change makes worse: findings base already had are `preexisting`. Tests that fail or hang on base go to a per-project quarantine. A run that didn't cover the suite is `error`, never "all tests pass". `--reuse` returns the report a clean run already stored for the same head.
- **A gates profile per project**, kept in Thulu and never in your repository (`gates_profile` in `PATCH /projects/{id}`); `accept_unverified` lets a check that can't finish pass with a reason. A run's key can't change it.
- Validated against the real tools of JS/TS, Python, Rust and Java. eslint 9 runs on projects with only an `.eslintrc`.
- **A runner ships its own copy**: every job writes the embedded CLI first on `PATH`, so updating `thulu` updates the gates. A copy the shell finds earlier hands the run over.

### thulu-browser

- Sessions take `network` (`fast-4g`, `slow-4g`, `3g`, `offline`), `cpu_throttle` and `color_scheme`, and takes replay them. Each step is timed, `browser_metrics` gives TTFB, FCP, LCP, CLS and INP, and `browser_findings` ties console errors and 4xx/5xx responses to the step that caused them. `evidence_verify(check="perf")` holds a metric under a maximum on every replay.
- `a11y_scan` runs axe-core against WCAG 2.2 AA. `browser_new_session(blind=true)` shows the agent only what a screen reader announces.

### Runners

- **A run can't leave processes behind.** The runner tracks every process of a host run, including servers an agent started in another session, and kills them all when the run ends; after a crash it finds and kills what earlier runs left. Runs stop 2 minutes past the workspace's time limit and when the daemon stays silent past the lease plus 5 minutes.
- **Memory caps**: each run has a limit for everything it starts (`run_memory_limit_mb`, 4096 by default); below `machine_memory_floor_mb` the runner stops the largest run, and below `min_free_memory_mb` it takes no new runs and says so in Settings → Runners.
- Temporary files stay in the run's `$TMPDIR`; what a run leaves in `/tmp` is removed when it provably belongs to the run, and containers it created in its folders are removed. Worktrees of closed tasks are reclaimed, unpushed work kept as a ref first, and oversized build caches are trimmed. `thulu runner doctor` shows the work folder's size.
- **Which MCP servers a run gets**: `local_tools` in `runner.json` (`thulu`, `selected` or `all`), set with `thulu runner config local-tools`; `thulu runner mcp import`, `add`, `remove` and `list`. A Claude Code policy that disables bypass mode is detected and the run uses `dontAsk` with `allowed_tools`. `thulu runner doctor` shows mode, MCP servers and why any are left out.
- **`thulu update` replaces every old copy** of the CLI on `PATH` (renaming a new file over it, since copying over a signed binary got it killed on macOS) and says when the runner service needs `thulu runner service restart` or `install`. `thulu runner service install` keeps the service file's `PATH` and variables.
- A daemon restart no longer kills runs on a runner: they get a minute to renew their lease. Locked tests are enforced on runner projects. When a run's outputs exceed the upload limit, the verdict and control files go first.

### Docker and Kubernetes

- After a restart the daemon re-arms the exit and deadline of every live run and reconciles every 2 minutes; on Kubernetes it adopts run pods by name. Before, a run that ended after a restart was never finished.
- Deleting a task stops its agents. Sandbox containers and pods no row records are removed; sandbox and chat pods get a 24-hour deadline; what a run or chat turn left running in the sandbox is killed when it ends, also after a restart. On Kubernetes, a server an agent left in the background no longer keeps the pod alive.
- The sandbox's `/tmp` is a tmpfs sized to memory (at most 2 GB, a quarter of the sandbox cap and an eighth of the Docker host's memory); `OCTOPUS_SANDBOX_TMP_SIZE` sets it, `off` goes back to disk.
- In Docker, the daemon checks at start and every 2 minutes that the configured sandbox and agent images exist, and lists a missing one under Infra → Problems with the command that builds it.

### Security

- **A run never receives a personal access token.** On a project connected with a PAT, thulu-github calls GitHub through the daemon's proxy, which forwards only the REST routes and GraphQL documents of a fixed table, after checking that the run's column grants a tool that makes that call, that its approval is given, and that the repository belongs to the task.
- A run's key can no longer move, retype or move to another board the task it works on, which let a run skip later stages and their approvals.
- Giving an agent access and approving criteria take a signed-in person, never an API key.

### Fixed

- **A slow database no longer stops the only Docker daemon.** A lease heartbeat that took over 3 s closed the session holding the lock and the daemon exited with `lost the daemon lease` though no other daemon existed. Kubernetes keeps exiting so the next pod takes over.
- Moving a task while its agent works no longer leaves two agents on the same branch: the next column's agent waits for the first to finish. A run that ends after its task moved keeps its work, but its result is not applied.
- Dragging a card no longer unfolds the folded columns.
- The dashboard works when the daemon listens on a port other than 8585.
- `OCTOPUS_LOCAL_MCP_CONFIG` in a runner's environment is honored again.

## [1.1.0] - 2026-11-17

Agents run on your own machine with its CLI login, and the SaaS bills agent hours instead of runs.

### Changed

- **Projects from a folder on your machine.** New project has a third origin, "On my machine": pick a machine with a runner and one of the git folders it reports, with its branch. Folders that already are projects fold away, and for a repository the list doesn't show the form offers `thulu add` to copy; if someone runs it while the form is open, "<project> was created from the terminal" appears with a button to open it. With no machine yet, the form shows the commands with a one-time token and the list updates when it connects. Settings → Runners lists the machines with their status, last contact, allowed folders and projects, adds one and revokes them. Projects on a machine show "On <machine>" in Projects, on the board card and on the task; a project's settings say where its agents run, in Thulu or on a machine.
- **The runner only reaches folders its owner allows** (`thulu runner allow`, `disallow`, `folders`, or `thulu init`). Every minute it reports the repositories inside them (`PUT /runner/v1/inventory`, read back with `GET /runners/{id}/repos`), and before each run it checks again that the project's folder is inside one, following symlinks. The daemon stores the path on the project (`runner_path`) and sends it with the job, so `thulu runner add-project` is no longer needed; projects that used it adopt their folder the first time the runner reports.
- A new workspace's board has a Review column between In progress and Done, with no agent, to check the work before closing it. Existing boards don't change.
- Runs on a machine use the model and effort they would in Thulu: the profile bound to the agent first, then the agent's own, then the default profile's. The profile's key never leaves the daemon, since the machine's login pays; before, an agent without its own model ran with `sonnet`.
- **`thulu add`** run inside a repository makes it a project on that machine's runner (`POST /runner/v1/projects`), named after the folder or `-name`. If the folder wasn't allowed it allows it, since running the command on the machine is the consent; running it again returns the existing project. A runner that is already running now picks up folders allowed meanwhile (`thulu add`, `thulu runner allow`) on its next claim or report instead of after a restart.
- The new-task form warns when a task mixes projects on different machines. Getting started counts the model key as done when every project is on a runner, and the missing-credential error names that way out too.
- **Machine profiles in AI engines.** The Claude Code card offers a new credential kind, "Login on a machine": pick a machine registered in the workspace and the profile holds no key, because the `claude` login on that machine pays. It can be bound to agents or be the engine's default like any profile; as the default it sends every Claude Code agent without a profile of its own to that machine. A profile keeps its kind (`profile_kind_locked`) but can move to another machine. Codex and OpenCode don't offer it yet.
- **`thulu init` offers the machine profile.** When it installs the runner, it asks whether Claude Code runs should use that machine's `claude` session and, on yes, creates "Claude Code on <machine>" as the default profile. The suggested answer is yes unless the same install loaded a Claude key. A rerun doesn't add a second profile. If the daemon refuses it, it lands among the pending steps with where to create it by hand.
- The runner reports, every minute, which engines it has installed and which account each is signed in with. The profile row shows the machine and that state, and suggests running `claude auth login` there when `claude` has no session. `thulu runner run` no longer requires `claude`, only one installed engine; a job for an engine the machine lacks fails naming it.
- **Where a run goes is decided once**, when its credential is resolved: the task's projects on a machine choose first, then a machine profile, then a container. A project on one machine and a profile on another refuse the run naming both. A revoked, disconnected or unknown machine refuses the run at launch instead of leaving it queued for ten minutes until it's marked lost. On a machine the personal login doesn't apply, and the engine's credential variables exported in the person's shell (such as `ANTHROPIC_API_KEY`) are removed before the engine starts, so they can't outrank the login.
- **Thulu projects run on a machine.** The runner fetches the code from `/git/<task>/<project>` with the run's key, which a credential helper hands to git, so it never lands in `.git/config` or a URL. It keeps one mirror per project under `<work>/repos` and starts every job from a clone of its own at the freshly fetched tip. When the run ends, however it ends, it pushes the work to `octopus-result-<root>` (the root task, also for a subtask); the daemon integrates it into the task branch and deletes that branch, so a run that fails before pushing can't bring the previous result back. A task can now mix projects on a machine with Thulu projects.
- **That final push only integrates.** GitHub gets the result when the run ended well and the column pushes, as on Docker. A `git push` the agent makes during the run is published right away, so `pr_create` finds the branch. If the final push fails (lid closed, tunnel down, daemon restarting), the work waits in the machine's mirror (`refs/octopus/unsent/...`) and goes with the task's next run there; after a runner crash, it saves leftover clones before cleaning up. The clone commits as Thulu and unsigned when the machine has no identity or signs through a dialog. The first fetch of a large repository no longer stops at 60 s; it only stops after a minute without progress.
- Kubernetes: a pod's final push no longer publishes to GitHub either. A failed or canceled run, or a column without "Push the branch", no longer reaches GitHub when it ends.
- **Live messages reach an agent running on a machine.** The runner long-polls them (`POST /runner/v1/jobs/{id}/steer`) and writes them into the engine's queue. If the engine already sealed the queue or the machine doesn't answer within 10 s, the message waits for the continuation, as on Docker.
- **Chat on the machine.** A chat runs on the machine when its runs would: an agent with a machine profile, or the chat of a task with projects on a machine. Each message is a `chat_turn` job the runner runs in `<work>/chats/<chat>`, a folder that outlives the turns and keeps the Claude session, removed after 30 days without turns. A task's chat reads the task's code and can't change it, and no chat can push through `/git/`, delegated or not. Turns have their own slots (`chat_concurrency` in `runner.json`, 2 by default), so they answer while runs fill every slot, and they count against `chat.max` like any chat. Moving the profile to or from a machine recreates the chat where it runs now. A task's chat on a disconnected machine, or on a daemon with runners off, is refused with the reason instead of running in a container without the machine's code. A deleted task's chat keeps working, placed by its profile. A turn whose answer never arrives from the machine fails instead of being saved empty, and a restarted daemon cancels the turns left queued or running.
- The machine profile row says "Not reported yet" while the runner hasn't started and "Runners off" when the daemon has them off, and refreshes while the tab is open. `GET /engines` returns each engine's login command (`machine_login_command`). Revoking a machine warns which profiles can no longer run.
- **Runner protocol 2, with protocol 1 still served.** The daemon records the protocol each runner declares and hands it only the jobs it understands, so an old runner keeps running folder projects. A Thulu project or a chat on a protocol 1 runner is refused at launch with "update thulu on that machine"; a run with live messages still goes, and its messages wait for the continuation.

- **SaaS: agent hours replace runs.** A workspace's plan includes agent hours per calendar month (UTC) instead of a run quota: 200 on Starter and 100 per agent on Team; the 30-day trial includes 50 hours for the whole trial. With `OCTOPUS_SAAS_MODE=1` on Kubernetes, the daemon samples agent, chat, auditor and Evidence pods every 30 seconds (a sandbox counts half), from the first container starting to the last one stopping; waiting for a node and pulling images don't count. Stage runs, mentions, chats and Evidence batches ask before they start, and a run already going when the hours run out finishes. The task explains a refused launch, and the first launch past 80% and 100% of the included hours posts a notice.
- **SaaS: extra hours with a spending cap.** Past the included hours each extra hour costs $0.05, up to a monthly cap a workspace admin sets in Settings → AI engines (`PUT /entitlements/extra-hours-cap`; $5 per agent by default, 0 turns extra hours off). Closed months go to Stripe as meter events in whole hours, never past the cap, billed with the terms the month had while it was paid.
- **SaaS: plans.** `trial`, `starter`, `team`, `enterprise` (by agents or dedicated nodes) and `inactive`. Old `free` rows read as `trial` and `business` as `team`. The Stripe webhook reads plan, agents and nodes from the subscription metadata; `past_due` keeps the plan while Stripe retries, `incomplete` waits for the first payment, and `canceled`, `unpaid` or `incomplete_expired` stop new runs.
- **SaaS: seats count members only.** The trial's and Starter's cap of 4 is checked when someone accepts an invitation; a pending invitation takes no seat. If the workspace is full, the person doesn't get in, the login page says so, and the invitation stays pending for a later sign-in. Self-hosted never applies a seat limit, on Docker or Kubernetes.

### Security

- Dependency updates for published advisories: the MCP SDK 1.32 and its transitive packages (`body-parser`, `qs`, `hono`, `@hono/node-server`, `fast-uri`, `ip-address`) in every MCP server, zod 4 in the browser MCP server, `go-jose` 4.1.9 and the OpenTelemetry exporters 1.47. govulncheck and `npm audit --omit=dev` report nothing reachable.

### Fixed

- **A runner job keeps its lease for the whole job.** The heartbeat now starts when the job is claimed and stops after the final report. Before, fetching the code, pushing and uploading results ran without it, and a slow clone or push could end with the run marked lost.
- **The dashboard stopped updating live after a drop**: a daemon restart, the machine going to sleep, or a network blip. The browser reconnected on its own with the opening URL, and the page didn't ask again for the topics it had added later, like the chat, so replies only showed up after a reload. Now, after every reconnect, it asks again for whatever is missing.
- **A job for the machine (a run or a chat turn) could be lost.** If the daemon took it right as the runner's 25-second wait ran out, the job stayed taken but the runner got an empty answer, and a minute later it failed with "runner … stopped answering (or never took the run)". Now a claim that has started finishes and hands the job over. Taking the job and dropping its key and message is one operation, and if the runner has already hung up, the job stays queued.
- **A run that got a live message mid-turn never ended.** Claude Code folds that message into the running turn and answers both with one result, but `steer-driver.js` waited for one result per message and never closed the engine's input. It now takes the count from `queued_turn_count`. This happened on Docker and on a machine.
- **A live message sent before the engine started was lost**: `entrypoint-base.sh` emptied the queue on start. It now creates the queue only when it's missing, on Docker and on a machine.
- **Login and dashboard no longer bounce off each other.** A leftover session cookie (an expired session, or another daemon that answered on the same host name) sent the login page on to the dashboard and the dashboard back to the login page, hundreds of times a minute, until the rate limit answered everything with `429`. The login page now moves on only when the daemon confirms the session (`GET /auth/me`, refreshing if needed); otherwise it drops the leftover CSRF cookie and shows the sign-in, or the `thulu open` hint. A `429` on the refresh or on that check waits out `Retry-After` (up to 5 s) and retries twice instead of reading as signed out, and a tab sent back to the login page three times in 30 s stays there with a notice.
- **Your own Claude Code login.** The personal sign-in moved into the Claude Code card in Settings → AI engines ("Your own account"). "Use my account" opens a three-step dialog that counts the seconds while Thulu gets Claude Code ready (the first time it installs it, about 12 s), then shows the link and asks for the code. `POST /me/engine-logins/{engine}` now answers `202` at once and the daemon keeps preparing on its own, so a reload no longer aborts it or leaves an empty login volume behind; the card offers "Continue", and the link comes from the new `GET /me/engine-logins/{engine}/flow`. An empty login volume read as "the CLI command ended with code 1", because `claude auth status` exits 1 when nothing is stored; it now reads as "Not connected".
- Release images no longer carry the SLSA provenance as a registry referrer. cosign 2.6 and later verified that referrer instead of the image signature, so `cosign verify --key` and `cosign verify-attestation --type spdxjson` failed on correctly signed 1.0.0 images until those referrers were removed from ghcr.io on 2026-11-11; 1.0.0 verifies with the documented commands again. The provenance stays in GitHub's attestation store: `gh attestation verify oci://ghcr.io/misterdonnuts/<image>:<version> -R MisterDonnuts/thulu`.

## [1.0.0] - 2026-11-11

First public release.

### Core

- **Stages with their own agent.** Each stage of a board runs the agent
  assigned to it. Your team creates or imports its agents; none come with the
  install.
- **An auditor that can kick back or block.** Turned on per agent and stage
  (none is on in a new install), an auditor reviews a run and passes it, sends
  the task back to another stage or blocks it. Each stage can pick its engine
  and model, and an auditor can use a different engine from its agent.
- **Approvals only a person grants.** A stage can require approvals, an agent
  can request one and a person can add one; any of them holds the task. Only
  a person with the developer role or above decides; an agent never approves.
  The factory board ships with none.
- **Agent questions.** `task_ask` lets an agent ask a person and wait. A
  blocking question parks the task in `awaiting_input` with no container
  running, and the agent resumes the same session.
- **Hash-chained audit log.** One per workspace, verifiable. The external copy
  goes to stdout, stderr or an NDJSON file; there is no connector to a SIEM.
- **LLM gateway.** On by default in Docker Compose and on Kubernetes, the agent's
  container never sees the real model key: it talks to the daemon, which adds it. A Claude Pro/Max OAuth token does
  not go through the gateway.

### License and editions

- The core is AGPL-3.0-only. What lives in `ee/` is under the Thulu
  Enterprise License and turns on with a license in `OCTOPUS_LICENSE_KEY`
  (`secrets.licenseKey` in Helm). Without one, the same binaries and images run
  as Community Edition.
- A license is needed for the Kubernetes runtime (`kubernetes`),
  `GET /audit/export` (`audit_export`) and the Stripe webhook (`saas`). An
  install with `OCTOPUS_AGENT_RUNTIME=kubernetes` and no license that includes
  `kubernetes` does not start.
- Workspaces, people and runs have no limit in either edition.
- Settings → Members tells owners and admins when an install has more people
  than its plan covers (4 without a license, or the license's `seats`). It
  blocks nothing and sends nothing out of the install.
- The license is verified offline against the public key built into the
  binary. An expired license keeps its features for 14 days, then the daemon
  continues as Community and says so in the log.
- `go build -tags foss .` builds the core without `ee/`. See
  `docs/ee-licensing.md`.

### Also included

- **Boards and stages.** Tasks move through customizable stages, each with its
  own agent, MCP servers and approval gates. Task detail with activity,
  subtasks, comment threads, pull requests and test cases.
- **Agent runs.** Claude Code is the main engine; Codex and OpenCode also run
  agents and auditors. Live messages to a running agent and the runner need
  Claude Code. Runs happen in Docker or (with a license) in Kubernetes pods
  with a sandbox per task, on linux/amd64 and linux/arm64 images. Each workspace
  has its own provider keys: Anthropic, Amazon Bedrock or Google Vertex, plus
  OpenAI and OpenAI-compatible APIs for Codex and OpenCode.
- **Runner.** `thulu runner` runs a project's agents on a person's own
  machine with its git, tools and login, on its own clone of the repository.
  Off by default on Kubernetes (`OCTOPUS_RUNNERS`); no UI.
- **`thulu init`.** Interactive setup with forms, a quick mode and the
  runner as a service. `install.sh` installs only the CLI, and
  `thulu init` downloads what the chosen mode needs.
- **MCP proxy.** Agents reach MCP servers through the daemon proxy, with
  per-tool permissions per stage.
- **Bundled MCP servers.** `octopus-api` (61 tools: tasks, boards, comments,
  approvals), `thulu-github` (42: PRs, reviews, Actions), `thulu-browser` (29:
  browsing, screenshots and recorded video), `thulu-sandbox` (7: commands,
  preview URL, init script), `thulu-cloud` (6: read-only cloud observability)
  and `thulu-figma` (6: read-only Figma files).
- **Workshop.** Install agents and skills from the catalog, or import and
  export them as JSON. Each workspace has its own catalog.
- **Projects.** Clone from a Git host or create one by uploading a folder.
  Init scripts with a setup phase, tool versions through mise and a
  per-workspace dependency cache.
- **Cost and limits.** Cost per run, per tool and per delegation; a time limit
  per run for each workspace; a per-workspace quota on what pods reserve, with
  a queue when there is no room (Kubernetes runtime).
- **Workspaces and auth.** Sign-in delegated to an OIDC provider or GitHub;
  roles, workspace switcher and invitations by email (Thulu gives you the
  link to share; it sends no mail); API keys scoped to one
  workspace with explicit permissions.
- **Delegated chat.** A chat can act for the person for a few hours with a
  key limited to tasks, and asks before deleting anything.
- **Updates.** `thulu update` reads a signed release manifest; migrations
  follow expand/contract so the previous release runs on the new schema
  (`docs/migrations.md`).
- **Languages.** Dashboard, CLI, MCP servers and scripts in English and
  Spanish (`OCTOPUS_LOCALE`).
