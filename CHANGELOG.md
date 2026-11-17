# Changelog

All notable changes to Thulu are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

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
