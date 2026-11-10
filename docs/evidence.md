# Evidence

Evidence is the workspace's library of test cases that film themselves. A case
says in plain words what has to work; once an agent has recorded it, Thulu
replays its take (a JSON script of browser steps with checks) against any
environment without a model, and every run leaves a video, annotated captures
and a pass/fail per check. Cases live on their own: no task is needed to
write, record or run one. Task test cases (`docs/test-cases.md`) keep working
and can be copied into the library.

The screen is **Evidence** (`/evidence/`, es: "Evidencia"). The cross-task view of task test cases is its "From tasks" tab.

## Who it is for

QA people, product managers and whoever signs off a release: they write what
must work, ask an agent to record it once, and then watch runs. They should
never need to read a take, a selector or a log to know whether the app works.

## Concepts

| Concept | What it is |
|---|---|
| Case | What has to work: title, context, steps, expected result, priority, tags, optional project. Has a take once recorded. Key `EV-<n>`, unique per workspace. |
| Take | The recorded browser script (thulu-browser take format, `tools/thulu-browser/evidence/schema.js`). Same-origin URLs are paths, so it runs against any environment. |
| Environment | Where cases run: the testing side of a project environment, a project's Sandbox, or a site with no project (see Environments). |
| Run | One case replayed on one environment: status, checks, failures, duration, video, captures, report. |
| Batch | The runs started together (one click, one schedule tick, one deploy). One runner works through a batch. |
| Recording | A chat in which an agent records a case's take on an environment. |

## Environments

An Evidence environment is one of three kinds:

| kind | what it is | URL | name |
|---|---|---|---|
| `project` | the testing side of one project environment (project page, Environments tab) | the project environment's URL | the project environment's display name, else its name |
| `sandbox` | the project's Sandbox: a fresh copy of the project brought up from a branch for one batch | the Sandbox's preview URL, known once it is up | "Sandbox" |
| `standalone` | a site with no project in Thulu (a vendor's staging, a site without a repo) | its own | its own |

Evidence keeps what only testing needs: extra allowed hosts (CDN, auth
provider), test logins (sealed, write-only), a schedule, and for `project`
rows "run after a deploy". Name, URL and the production flag of a `project`
row live in the project environment; editing them in Evidence writes them
there.

Thulu creates the rows by itself: one `project` row for each project
environment and one `sandbox` row for each project, on every minute tick and
whenever Evidence lists environments. Creating an environment from Evidence
with a project creates the project environment (provider `other`) and its row
in one step.

Which cases run where:

| environment | runs |
|---|---|
| `project` | cases with no project, and cases of the environment's project |
| `sandbox` | cases of the environment's project only |
| `standalone` | every case |

The same rule decides which cases a batch queues, which environments "Run
on…" and "Record with agent" offer, and which cases the case list shows for
an environment.

Deleting a project environment on the project page keeps its Evidence row as
a `standalone` environment: same id, runs, videos, test logins and schedule,
its last URL and production flag, "run after a deploy" off, and as name its
display name (or name) followed by ` (<project>)`, with a counter if that is
taken. Deleting a
project does the same with each of its environments, deletes its Sandbox row
with that row's runs and files, and leaves its cases without a project (they
keep their runs). An admin can delete a standalone environment from Evidence,
or link it to a project environment that has not run yet
(`POST /evidence/environments/{id}/link`): the row keeps its id, runs, logins
and schedule and becomes that project environment's.

### Sandbox

The Sandbox runs a project's cases against a copy of the app that Thulu
brings up for the batch. Running on it takes a branch (default: the
project's default branch). Thulu fetches the branch with the project's
read-only credential in the daemon, hands the tree to a sandbox without
credentials, runs the project's init script (tools, setup with the workspace
cache, start, ready check), and points the runner at the preview URL of the
first init script port that sandboxes expose. The sandbox is torn down when
the batch ends, fails or is canceled.

The Sandbox is available when the install has a sandbox runtime, the project
has an init script, and one of the script's ports is one sandboxes expose.
Otherwise running on it is refused with the reason (`sandbox_unavailable`,
`no_init_script`, `no_preview_port`). The Sandbox has no schedule and no
deploy trigger, and recording on it is not offered: a recording chat lasts as
long as a person wants, and nothing keeps a sandbox alive for it. Its only
editable fields are allowed hosts and test logins.

## Permissions

The API enforces these rules and returns them per environment in `can`, so
the UI disables what the caller cannot do and shows the reason.

| action | viewer | developer | admin, owner |
|---|---|---|---|
| read cases, runs, batches, videos, environments | yes | yes | yes |
| create, edit, delete cases; upload or clear a take | no | yes | yes |
| record (not on a Sandbox) | no | yes | yes |
| run a batch (any environment, Sandbox included) | no | yes | yes |
| create an environment (project or standalone), non-production | no | yes | yes |
| create a production environment | no | no | yes |
| edit the testing side (URL, display name, hosts, schedule, after-deploy) of a non-production environment | no | yes | yes |
| same, production | no | no | yes |
| replace test logins (values never readable), non-production | no | yes | yes |
| same, production | no | no | yes |
| change `is_production` (standalone here; project rows on the project page) | no | no | yes |
| delete a standalone environment | no | no | yes |
| link a standalone environment to a project environment | no | no | yes |

`project` rows are deleted on the project page, and the Sandbox row is never
deleted. The caller's role is the session's role, or the API key's role (run
keys count as developer at most).

Reason codes, in `can` and in refusals (`api.evidence.errors.forbidden_<code>`;
the UI translates them as `evidence.can.<code>`):

| code | when | status |
|---|---|---|
| `viewer` | the caller is a viewer | 403 |
| `production` | the environment is production and the caller is a developer | 403 |
| `admin_only` | the action needs admin or owner (delete, link) | 403 |
| `delete_on_project` | a `project` row: delete it on the project page | 409 |
| `sandbox_fixed` | a `sandbox` row cannot be deleted or linked | 409 |
| `sandbox_no_record` | recording on a Sandbox is not offered | 409 |
| `no_url` | a `project` row whose project environment has no URL | 412 |
| `no_init_script` | the project has no init script (Sandbox) | 412 |
| `no_preview_port` | none of the init script's ports is one sandboxes expose (Sandbox) | 412 |
| `sandbox_unavailable` | no sandbox runtime in this install (Sandbox) | 412 |
| `repo_not_github` | after-deploy: the project's repo is not on GitHub (in `deploy.reason`) | |

## Model (component `evidence`)

`evidence_environments`

| column | type | notes |
|---|---|---|
| id | text pk | |
| workspace_id | text | |
| kind | text | `project`, `sandbox` or `standalone` (default) |
| project_id | text | set for `project` and `sandbox` rows |
| project_environment_id | text | set for `project` rows |
| name | text | standalone: unique per workspace among standalone rows, ≤ 60 chars; project: a copy of the project environment's name, read only if the row becomes standalone; sandbox: `sandbox` |
| base_url | text | standalone: http(s) URL, no userinfo; project: a copy of the project environment's URL, read only if the row becomes standalone; sandbox: empty |
| is_production | boolean | read for standalone rows; a project row reads the project environment's flag; a Sandbox is never production |
| on_deploy | boolean | project rows: run after a GitHub deployment of this environment |
| allowed_hosts | jsonb | extra hosts a replay may reach (CDN, auth provider) |
| secrets_sealed | text | secretbox-sealed JSON `{name: value}`; never leaves the daemon except to a runner or recording browser of this workspace |
| secret_names | jsonb | the names, for the UI |
| schedule | text | 5-field cron (automation syntax) or `''`; not used on sandbox rows |
| schedule_tz | text | IANA zone, default `UTC` |
| schedule_project_id, schedule_tag | text | standalone rows: a scheduled batch runs only the cases of this project and/or with this tag; `''` runs them all |
| last_scheduled_at | timestamptz null | |
| created_by, created_at, updated_at | | |

One row per project environment and one `sandbox` row per project (partial
unique indexes).

`evidence_cases`

| column | type | notes |
|---|---|---|
| id | text pk | |
| workspace_id | text | |
| project_id | text | `''` or a project of the workspace; decides where the case runs (see Environments) |
| key | text | `EV-<n>`, unique per workspace, assigned max+1 |
| title | text | required, ≤ 200 |
| description, steps, expected | text | markdown |
| priority | text | low, medium (default), high, critical |
| tags | jsonb | array of short strings |
| take | jsonb | `null` until recorded |
| take_source | text | `agent`, `task`, `upload` or `''` |
| take_updated_at | timestamptz null | |
| source_task_id, source_case_key | text | when copied from a task case |
| status | text | `draft` (no take), `ready` (take, never run), or the last run's status |
| last_run_at | timestamptz null | |
| created_by, updated_by, created_at, updated_at | | |

`evidence_batches`: id, workspace_id, environment_id, trigger (`manual`,
`schedule`, `deploy`, `api`, `recording`), trigger_ref (who or what: an email,
`owner/name@sha · <GitHub environment>`), status (`queued`, `running`,
`done`, `canceled`, `error`), counts (total, passed, failed, blocked, errored,
canceled), runner (pod or container name), error, created_by, created_at,
started_at, finished_at, base_url (the URL the batch ran against, frozen when
it is queued, or when its Sandbox comes up), locale (the language of the
person who queued it; the runner reports in it), and for Sandbox batches
source_branch, source_commit, sandbox_status (`''` for other batches,
`pending`, `starting`, `ready`, `failed`, `released`), sandbox_phase (while
starting: `room`, `source`, `tools`, `setup`, `start`), sandbox_error,
sandbox_reason, sandbox_reason_args, sandbox_log (the last 3000 bytes of the
init script's output when it failed).

`evidence_runs`: id, workspace_id, batch_id, case_id, environment_id, position,
status (`queued`, `running`, `pass`, `fail`, `blocked`, `error`, `canceled`),
checks jsonb (`[{label, expected, observed, pass, measured}]`), failures jsonb
(`[{step, kind, label, error}]`), error text, duration_ms, take jsonb (the take
as run), report_md text, video_id, take_id, chapters_id, poster_id (artifact
ids or `''`), shot_ids jsonb, created_at, started_at, finished_at.

`evidence_artifacts`: id, workspace_id, run_id, kind (`video`, `take`,
`chapters`, `shot`, `poster`), filename, mime_type, size, path, created_at.
Files live under `<dataDir>/evidence/<workspace>/<run>/`; they count against the
workspace storage quota together with task attachments. Deleting a case
deletes its runs and their files.

`evidence_recordings`: id, workspace_id, case_id, environment_id, chat_id,
status (`open`, `saved`, `closed`), created_by, created_at, saved_at.

Statuses of a run: `pass` every check ran and passed; `fail` a check failed;
`blocked` a step failed and kept a planned check from running; `error` the
replay could not run (runner crashed, environment unreachable, take invalid);
`canceled`.

## API

Session or operator-key routes are in the `tasks` permission group (QA work is
task work); runner routes are in `runs`, which run keys hold. `{case}` is the
id or the key. The role is the route's minimum; the handler then applies the
per-environment rules of Permissions.

| Route | Verb | Role | Does |
|---|---|---|---|
| `/evidence/context` | GET | viewer | the caller's role, workspace-level `can`, the projects with their case and environment counts and Sandbox availability, `unassigned_cases`, `standalone_environments`, `show_project_picker` (more than one project) |
| `/evidence/summary` | GET | viewer | `?project_id=&include_unassigned=1` → `{cases, recorded, counts, running, last_batch}`: cases by status, how many have a take, runs queued or running, the latest batch |
| `/evidence/cases` | GET | viewer | `?q=&status=&project_id=&include_unassigned=&tag=&priority=&environment_id=` → `{cases, counts, tags}`; `tag` and `priority` take a comma list (every tag, any priority); `project_id=none` is the cases without a project; `include_unassigned=1` adds them to a `project_id` filter. With `environment_id` only the cases that run there are listed, and `status` and `counts` are each case's latest result there, `not_run` when it never ran there (another workspace's environment is 404). `tags` lists the tags of the cases in the project scope, other filters aside. Each case has `latest: {<env_id>: {run_id, status, finished_at, poster_id}}` and no take body (`has_take` instead) |
| `/evidence/cases` | POST | developer | create (`title` required, `project_id` and `take` optional); returns the case |
| `/evidence/cases/{case}` | GET | viewer | `{case, recordings}`: the case with its take and its last 20 runs, and its recordings |
| `/evidence/cases/{case}` | PATCH | developer | title, description, steps, expected, priority, tags, project_id |
| `/evidence/cases/{case}` | DELETE | developer | case, runs, files |
| `/evidence/cases/{case}/take` | PUT | developer | upload a take (JSON body = the take); validated; `take_source = upload` |
| `/evidence/cases/{case}/take` | DELETE | developer | back to draft |
| `/evidence/cases/from-task` | POST | developer | `{task_id, case}`: copies a task case, and the take of its latest run when it has one |
| `/evidence/cases/{case}/record` | POST | developer | `{environment_id, agent_type?}` → `{recording, chat_id}`: opens a chat with an agent that records the take (see Recording); refuses a Sandbox (409 `sandbox_no_record`) and an environment the case does not run on (409 `case_not_on_env`) |
| `/evidence/recording-agents` | GET | developer | `{agents, auto}`: the agents that can record (chat stage), each with `browser`, `ready` (model credential), `prompt_ready` (chat prompt) and `reason`; `auto` is the one picked when none is given |
| `/evidence/environments` | GET | viewer | `?project_id=<id>\|none` (`none`: standalone only; empty: all) → `{environments}`, every kind, grouped by project with the Sandbox last and standalone rows at the end; secret names, never values; each with `can`, `deploy` and `sandbox` where they apply, and `last_batch` |
| `/evidence/environments` | POST | developer | with `project_id`: `{project_id, name, display_name?, base_url, is_production?, allowed_hosts?, schedule?, schedule_tz?, on_deploy?}` creates the project environment and its row; without: `{name, base_url, is_production?, allowed_hosts?, schedule?, schedule_tz?, schedule_project_id?, schedule_tag?}` creates a standalone one. `is_production: true` needs admin |
| `/evidence/environments/{id}` | PATCH | developer | project: `display_name`, `base_url` (written to the project environment), `allowed_hosts`, `schedule`, `schedule_tz`, `on_deploy`. standalone: `name`, `base_url`, `allowed_hosts`, `schedule`, `schedule_tz`, `schedule_project_id`, `schedule_tag`, `is_production` (admin). sandbox: `allowed_hosts`. Any other field: 400 `field_not_editable`. Production: admin |
| `/evidence/environments/{id}` | DELETE | admin | standalone only; refuses while a batch is queued or running there |
| `/evidence/environments/{id}/secrets` | PUT | developer | `{name: value\|null}`: set or remove (null) values; names `^[A-Z][A-Z0-9_]{0,40}$`, at most 20; production needs admin |
| `/evidence/environments/{id}/link` | POST | admin | `{project_environment_id}`: a standalone row becomes that project environment's (keeps its id, runs and logins); the project environment's own row is deleted; 409 `link_target_has_runs` when that row has batches |
| `/evidence/runs` | POST | developer | `{environment_id, case_ids?, project_id?, include_unassigned?, tag?, branch?}`: queue a batch of the recorded cases that match and run on the environment (all of them when nothing narrows it) → the batch with its runs. Explicit `case_ids` that do not run there: 409 `case_not_on_env` naming them. `branch` only on a Sandbox (default: the project's default branch; elsewhere 400). A Sandbox that is not available: 412 with the reason. No URL: 412 `env_no_url`. Nothing matches: 412 `nothing_to_run` |
| `/evidence/environments/{id}/run` | POST | developer | the CI hook: same as above with this environment; `trigger = api`; on a Sandbox it accepts `branch` |
| `/evidence/batches` | GET | viewer | recent batches, newest first, `?environment_id=&project_id=&limit=` |
| `/evidence/batches/{id}` | GET | viewer | the batch and its runs, with the Sandbox fields and `sandbox_log` |
| `/evidence/batches/{id}/cancel` | POST | developer | queued runs → canceled; the runner stops after the current one; a Sandbox that is still starting is stopped and torn down |
| `/evidence/runs` | GET | viewer | `?case_id=&environment_id=&batch_id=&status=&limit=` newest first |
| `/evidence/runs/{id}` | GET | viewer | the run with checks, failures, report and artifact list |
| `/evidence/artifacts/{id}` | GET | viewer | the file, with range support (video seek) |

Errors on batches and runs come in the request's language. When a batch or
run failed for a reason on the runner's side, it carries `error_cause`:
`browser_image`, `runner_start`, `runner_exit`, `deadline`,
`daemon_restarted` or `runner_lost`.

An environment in `GET /evidence/environments`:

```json
{
  "id": "e1", "kind": "project",
  "project_id": "p1", "project_name": "web",
  "project_environment_id": "pe1",
  "name": "staging", "display_name": "Staging",
  "base_url": "https://staging.example.com", "is_production": false,
  "allowed_hosts": [], "secret_names": ["QA_EMAIL"],
  "schedule": "0 7 * * 1-5", "schedule_tz": "Europe/Berlin",
  "schedule_project_id": "", "schedule_tag": "",
  "on_deploy": true, "last_scheduled_at": null,
  "deploy": {"available": true, "repo": "example/web", "environment": "staging"},
  "sandbox": null,
  "can": {
    "run": {"allowed": true}, "record": {"allowed": true},
    "edit": {"allowed": true}, "secrets": {"allowed": true},
    "delete": {"allowed": false, "reason": "delete_on_project"},
    "link": {"allowed": false, "reason": "admin_only"}
  }
}
```

A `sandbox` row has `name: "sandbox"`, `display_name: ""`, `base_url: ""`,
`deploy: null` and `sandbox: {environment_id, available, reason, port,
default_branch, init_script_url}`. A `standalone` row has `project_id: ""`,
`deploy: null`, `sandbox: null`. The UI shows `display_name || name`.

Runner routes (the runner's key only; it names its batch):

| Route | Verb | Does |
|---|---|---|
| `/evidence/jobs/{batch}/next` | POST | `200 {run: {id, case_key, title}, take, base_url, allowed_hosts, secrets, sandbox, environment: {name, kind}}` or `204` when nothing is left; `base_url` is the batch's |
| `/evidence/jobs/{batch}/runs/{run}/artifacts` | POST | multipart `file` + `kind` → `{id}` |
| `/evidence/jobs/{batch}/runs/{run}/result` | POST | `{status, checks, failures, duration_ms, report_md, error, video_id, take_id, chapters_id, poster_id, shot_ids}` |
| `/evidence/jobs/{batch}/unreachable` | POST | `{error}`: the batch's URL never answered; closes it as an environment failure |

Recording routes (the recording chat's key only):

| Route | Verb | Does |
|---|---|---|
| `/evidence/recording` | GET | the open recording of the caller's chat: `recording`, `case` (key, title, description, steps, expected, has_take), `environment` (id, name, display_name, kind, project_name, is_production, base_url, allowed_hosts, secret names) |
| `/evidence/recording/secrets/{name}` | GET | one test login value, for thulu-browser's `browser_type(secret=…)` |
| `/evidence/recording/take` | PUT | `{take}`: save it as the case's take (`take_source = agent`), close the recording, queue a `recording` batch on its environment → `{case, batch, environment}` |

## Running a batch

Queuing a batch starts one runner for it: a pod in the workspace's namespace
(Kubernetes) or a container on the agent network (Docker), from the
thulu-browser image, command `node evidence/job.js`, with `OCTOPUS_API`,
`OCTOPUS_API_KEY` (a run key with agent `evidence:<batch>`, stage `evidence`,
revoked when the runner exits), `EVIDENCE_BATCH_ID` and `OCTOPUS_LOCALE`. The
runner opens the batch's URL once before its first case, retrying for 30 s;
if it never answers, it posts `/evidence/jobs/{batch}/unreachable` and exits,
and the batch closes as an environment failure. Otherwise it asks for the
next run, replays its take with `renderTake` against the batch's URL with the
environment's secrets, uploads the video, take, chapters, poster and
captures, posts the result, and asks again until it gets `204`. When the
runner exits, any run still queued or running becomes `error` and the batch
closes. A batch has 2 hours. At most `OCTOPUS_EVIDENCE_MAX_RUNNERS` (default
2) runners per workspace; further batches wait queued. Side-effect steps run
only when the take allows them (`allow_side_effects` is never set by the
runner): a take recorded with `changes_data` is refused with `error`.

Every batch freezes its URL when it is queued; an environment with no URL
cannot be queued (412 `env_no_url`). Each run copies the take, so recording a
case again does not change a replay already queued.

The runner gets the per-run browser's isolation: uid 1000, read-only root,
scratch `/tmp` and `/dev/shm`, the browser's memory and CPU limits; in Docker
also every capability dropped and `no-new-privileges`. In Kubernetes it gets
the tenant NetworkPolicy: with egress `open` it reaches public addresses on
TCP 443 only, so an `http://` environment needs the egress allowlist. In
Docker the container joins the agent network.

A daemon that restarts closes the batches its predecessor left running (their
runner keys and watches died with it), removes their runners, and tears down
the Sandboxes they used.

### Sandbox batches

A Sandbox batch takes a runner slot like any other, then brings the Sandbox
up before the runner starts. The batch shows the phase while it starts:
`room` (waiting for a free sandbox; retried every 15 s for up to 20 minutes),
`source` (fetching and copying the branch), `tools`, `setup` and `start` (the
init script). Bringing it up has 75 minutes. Once the app answers, the batch
stores the preview URL and the commit, and the runner's 2 hours start.

The Sandbox counts against the workspace's sandbox concurrency limit like a
task's, and in Kubernetes both its pod and the runner's count against the
tenant's ResourceQuota. The idle sweep (`sandbox.idle_evict_seconds`) never
evicts it: the runner tests the app over its preview URL, which does not
count as activity. In Kubernetes the runner pod carries the sandbox's root
label, which the sandbox's preview policies already admit; in Docker the
runner and the sandbox share the agent network.

A Sandbox that does not come up is not a case failure. The batch's queued
runs become `canceled`, the batch `error` ("Couldn't bring the environment
up: …"), `sandbox_status = failed`, and case statuses do not move. The reason
is stored as a code with its variables and read in the request's language:
`no_room`, `branch_not_found`, `source_failed`, `launch_failed`,
`no_init_script`, `tools_failed`, `setup_failed`, `start_failed` (with the
script's exit code), `ready_timeout`, `no_preview_port`, `params_missing` (a
required init script parameter has no default), `unavailable`, `restarted`
(the daemon restarted while it started), `unreachable` (the runner could not
open the URL) and `error`. A batch on any other environment whose runner
reports `unreachable` closes the same way, without the Sandbox fields.

## Schedules and deploys

Schedules tick every minute on `project` and `standalone` rows; a row whose
URL is empty does not fire, and the next tick tries again. A daemon that was
down fires once, not once per missed slot. On a standalone row
`schedule_project_id` and `schedule_tag` narrow what a scheduled batch runs.

"Run after a deploy" (`on_deploy`) listens to GitHub `deployment_status`
(state `success`) on the App webhook. It fires every `project` row with
`on_deploy` whose project's repo is the delivery's, whose workspace claimed
the App installation (another tenant with the same repo never fires), and
whose project environment matches the deployment's environment: its name
equals the GitHub name lowercased with spaces and underscores turned into
`-`, or its display name equals it ignoring case. The App must be subscribed
to `deployment_status`. GitHub `workflow_run` events do not start batches:
they name a workflow, not an environment. Teams that deploy without GitHub
deployments call `POST /evidence/environments/{id}/run` with an operator key
from their CI.

## Recording

`POST /evidence/cases/{case}/record` opens a chat (title "Record EV-3 · …")
with the chosen agent and posts the instructions as the first message. With
no agent given, Thulu picks the first agent with the chat stage that has a
model credential and a chat prompt, preferring one whose chat stage already
has thulu-browser. It checks the credential and the chat prompt before
opening the chat (412 when either is missing). The chat gets thulu-browser
and the `octopus-evidence-library` system skill whatever its stage grants.
The agent explores the environment, films the flow live with
`evidence_start` … `evidence_finish`, checks with `evidence_verify` (with
`check` + `target`, so replays measure), types test logins with
`browser_type(secret="NAME")` (thulu-browser fetches the value from
`/evidence/recording/secrets/NAME`; the model never sees it), and saves with
`evidence_save_case`, which sends the take to `PUT /evidence/recording/take`.
Saving queues a first run on the same environment, so the person sees the
replay pass (or not) without asking.

Recording works on `project` and `standalone` environments with a URL that
the case runs on. The values behind `secret` are test accounts: the recording
run's key can read them, so the UI asks for test credentials only.

## Web

`/evidence/` is built for people who do not read code:

- **Project picker** at the top, shown when the workspace has more than one
  project. "All projects" shows everything; picking a project shows its cases
  plus the ones without a project, and its environments plus its Sandbox plus
  the standalone ones. The choice is remembered in the browser.
- **Cases**: a grid/list of cases with the latest result per environment
  (poster frame of the last video, status, when), filters (status, project,
  environment, tag, priority, text), "New case", "Run all on…", and per case
  "Record with agent" / "Run on…". Opening a case shows its description, steps
  and expected result, the last run's video with chapters, the checks table,
  captures, and the run history per environment.
- **Run on… / Record with agent** list only the environments the case runs
  on, grouped by project, and do not preselect a production environment. The
  Sandbox shows a branch field with the default branch filled in; when it is
  not available it is disabled with its reason and, for a missing or unusable
  init script, a link to the project's init script.
- **Runs**: the feed of batches (trigger, environment, counts, duration), each
  expandable into its runs with video. A Sandbox batch shows its phase while
  it starts and, when it does not come up, the reason and the end of the init
  script's output, apart from failed cases.
- **Environments**: grouped by project, with the Sandbox and the standalone
  sites ("Other sites"). The form has, for a project environment, display
  name, short name (set once), URL, allowed hosts, test logins (write-only),
  schedule (presets: hourly, daily at…, weekdays at…, custom cron) and "Run
  after a deploy" with the repo and GitHub environment it listens to; the
  production flag is set on the project page. A standalone site has name,
  URL, production, allowed hosts, test logins, and a schedule with the
  project and tag it runs. Both show the CI hook as a copyable curl. The
  Sandbox form has allowed hosts and test logins only.
  Controls the caller cannot use are disabled with the reason from `can`.
  Admins can move a standalone site to a project environment.
- **From tasks**: the cross-task view of task test cases, with "Add to
  library" on cases that have a recorded take.

The project page's Environments tab shows each environment's testing summary
(URL, number of test logins, schedule) with a link to edit it in Evidence
(`/evidence/?tab=environments&env=<id>`), and a display name field.
