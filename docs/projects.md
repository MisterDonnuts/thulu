# Projects

## What a project is

A **project** is a persistent clone of a git repository. It serves as the code base for tasks: when a task is dispatched to an agent, the daemon creates a git worktree from the project's clone and mounts it inside the agent container.

Projects live under `<data-dir>/projects/<name>/`. Each project has exactly one local clone. The repository it clones is either a remote (`source: "git"`, the URL in `repo_url`) or one the daemon keeps itself for a folder someone uploaded (`source: "upload"`, see [Upload a folder](#upload-a-folder)).

---

## Project fields

```typescript
interface Project {
  id: string;
  name: string;                      // unique, no spaces
  path: string;                      // absolute host path to the clone
  repo_url: string;                  // git remote (HTTPS or SSH); "" for an upload
  source: "git" | "upload";          // where the code comes from
  default_branch: string;            // default: "main"
  description: string;               // shown in agent prompts at every run
  stage_template: Record<string, string>;  // stage → agent_type, copied to new tasks
  default_board_id: string;          // workflow new tasks start on ("" = instance default)
  created_at: string;
}
```

### `description`

Keep it terse but informative — it is injected into every agent prompt that works on this project. Example: `"TypeScript monorepo for the web dashboard. Owns /web and /packages. Do not modify /infra."`.

### `stage_template`

When a task is created with this project linked, its `stage_assignments` is pre-populated from `stage_template`. The values are names of agent types in the workspace's catalog. Tasks spanning multiple projects get the union of all their projects' stage templates (later projects override earlier ones for the same stage).

### `default_board_id`

The workflow tasks created against this project start on — a repo whose work is
always hotfixes shouldn't need every task to name the board. Empty defers to
the instance default.

It is only a default: the board is a property of the task, which owns it from
creation on. A task linking several projects whose defaults disagree is
rejected with a 400 asking for an explicit `board_id`, because picking one
would depend on the order the caller happened to list the projects in. Setting
it to a board that doesn't exist is refused up front.

---

## Create a project

```
POST /projects
Content-Type: application/json

{
  "name": "my-app",
  "repo_url": "https://github.com/org/my-app.git",
  "default_branch": "main",
  "description": "Main application repo",
  "stage_template": {
    "in_progress": "implementer"
  }
}
```

The daemon `git clone`s the repo synchronously. If the clone fails, the endpoint returns 500.

---

## Upload a folder

A project can start from a folder on the person's machine instead of a repository URL. The dashboard's create dialog has two sources, "Git repository" and "Upload folder"; the second takes a folder (drag and drop or the folder picker) or a `.zip`.

```
POST /projects/upload            (admin)
Content-Type: multipart/form-data

meta     {"name": "my-app", "description": "...", "default_board_id": "", "stage_template": {}}
archive  folder.tar.gz | project.zip
```

`meta` must come first. The archive is capped at 200 MiB compressed and counts against the workspace's storage quota; unpacked, a project holds at most 20,000 files and 1 GiB.

What is left out, both in the browser before sending and again in the daemon:

- dependency and cache folders wherever they appear: `node_modules`, `.venv`, `venv`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox`, `.gradle`, `.next`, `.nuxt`, `.turbo`, `.parcel-cache`, `.cache`, `__MACOSX`;
- likely secrets: `.env` and `.env.<x>` (except `.env.example`, `.sample`, `.template`, `.dist`, `.defaults`), `.netrc`, `.pgpass`, `.pypirc`, `.git-credentials`, `.htpasswd`, SSH private keys, `.aws/credentials`, and `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, `*.ppk`;
- symlinks, and `.DS_Store` / `Thumbs.db` / `desktop.ini` (not reported).

The response lists what was excluded and skipped. A `.zip` whose entries all sit under one top folder has that folder stripped.

If the folder carries a `.git` with commits, its branches, tags and history are kept, the branch its `HEAD` names becomes the default branch, and files that differ from that commit go on top as a new commit. Without history the daemon makes a first commit on `main`. Both commits are authored by `Thulu <noreply@thulu.dev>`.

The daemon keeps the repository bare at `<data-dir>/projects/.hosted/<project id>.git`, named after the id and never after the name the person typed, and clones it like any other project. Worktrees, the agent's pushes, the Kubernetes transport and the dependency prewarm all go through `origin` as usual. git may use the `file://` transport only from a clone carrying the daemon's `octopus-hosted` marker; tenant URLs stay limited to the remote policy.

There is no pull request for an uploaded project. Instead:

| Method | Path | Role | |
|---|---|---|---|
| `POST` | `/projects/{id}/upload` | developer | A new version of the folder (multipart, just `archive`), committed on the default branch. Files missing from the folder are deleted in that commit; the folder's own `.git` is ignored. `changed: false` when nothing differs |
| `GET` | `/projects/{id}/branches` | viewer | `{default_branch, branches}` of the hosted repository |
| `GET` | `/projects/{id}/archive?branch=` | viewer | A zip of a branch (default branch when absent) |
| `POST` | `/tasks/{id}/merge` | developer | Lands the task branch on `target` (default branch when empty) of the task's upload project, `project_id` when it has several. A fast-forward when `target` has not moved, otherwise a merge commit. `status` is `fast_forward`, `merged` or `up_to_date`; a conflict answers 409 with `conflicts` (file list) and writes nothing |

In the task panel the Branch row shows "Merge into…" for a task linked to an uploaded project, and hides Force PR when every linked project is an upload. An agent run's key cannot call the upload or merge routes; a person does that from the dashboard.

---

## Worktrees

When a task leaves its board's initial stage, the dispatcher calls `project.Store.CreateTaskWorktree(projectID, taskID)`:

1. Fetches `origin/<default_branch>`
2. Creates a worktree at `<data-dir>/projects/.worktrees/<project-name>--task-<task-id>/`
3. Checks out branch `task-<task-id>` (created from `default_branch` if it doesn't exist yet; a branch the janitor parked is checked out again with its work)

A task in the initial stage has no worktree. In Docker mode the worktree's `.git` file points at the clone's `.git` directory on the host; the launcher mounts that directory read-only at the same path, so `git log`, `git diff` and `git status` work inside the container but the agent cannot commit. The daemon commits the worktree at the end of every stage run and pushes when the stage has `push_branch` and the run exited 0; thulu-github's `commit` and `branch_push` run the same logic on demand. In Kubernetes mode the agent works on a clone in the sandbox pod (`/workspace/projects/<name>`) whose `origin` is the daemon's `/git/` mirror, and the daemon integrates the result after each run.

### Worktree path format

```
data/projects/.worktrees/<project-name>--task-<task-id>/
```

The task's `worktree_paths` field maps `project_id → host_path`. The dispatcher mounts each worktree as:

```
/workspace/projects/<sanitized-project-name>/
```

inside the agent container.

### Cleanup

Worktrees are removed when the task reaches a terminal stage, is deleted, or its project is deleted. The daemon never runs git inside the worktree to do it (`git worktree remove` would read the agent-writable `.git` file): it deletes the directory and runs `git worktree prune`.

---

## GitHub credentials

A project's GitHub access comes from the credential manager: an installation token minted from the GitHub App when the project is linked to an installation, its stored credential otherwise.

Agents' MCP servers retrieve the token at runtime via internal endpoints:

```
GET /internal/tasks/{id}/github-token
GET /internal/projects/{id}/github-token
GET /internal/github-token?repo=owner/name
```

These internal endpoints require the run's API key and are called by the `thulu-github` MCP container.

An App token carries the MCP's default tools (`metadata:read`, `contents:write`, `pull_requests:write`, `actions:read`). `?permissions=` adds one or more of `actions:write` (`run_rerun`, `run_cancel`, `run_dispatch`), `deployments:write` (`deployment_approve`) and `checks:read` (`pr_checks`), each only when the run's stage grants thulu-github a tool that needs it (403 otherwise). An installation that was never granted a permission refuses the mint: the endpoint then answers with the project's stored credential, or `404` when there is none. For `checks:read` the MCP asks again without it and reads CI from GitHub Actions.

---

## Stage sidecar files

Each project can have per-stage sidecar files: a custom prompt and a pre-launch hook per stage. They are mounted alongside the worktree at `/workspace/projects/<name>/.thulu/`.

```
GET /projects/{id}/stage-files
GET /projects/{id}/stage-files/{kind}/{stage}    kind: prompts | hooks
PUT /projects/{id}/stage-files/{kind}/{stage}
DELETE /projects/{id}/stage-files/{kind}/{stage}
```

`prompts/<stage>.md` is injected into the agent's prompt for that stage;
`hooks/<stage>.sh` runs inside the worktree before the agent launches, with its
stdout, stderr and exit code appended to the prompt.

This is where per-stage commands live.

---

## Import / export

`thulu export` / `thulu import` carry a workspace's project rows (ids, stage templates, default boards, environments) to another install, but not the clone on disk or its local branches. An uploaded project is the exception: its hosted repository travels in the bundle under `code/` with every branch, since there is no remote to fetch it from. The code travels through the remote: `git push` the task branches you want to keep before moving or deleting the project. For a single project without the rest of the workspace, `POST /projects` with the same `repo_url` on the new instance; the clone includes every pushed branch.

---

## Data directory resolution

```
dataDir = OCTOPUS_DATA_DIR  (env)
        ‖ cfg.DataDir        (--data flag)
        ‖ cfg.AgentsDir + "/octopus/data"   (fallback)
```

Projects clone into `<dataDir>/projects/<name>/`. Worktrees go into `<dataDir>/projects/.worktrees/`.

---

## List worktrees

```
GET /projects/{id}/worktrees

→ [
    {
      "path": "/data/projects/.worktrees/my-app--task-abc1234",
      "branch": "task-abc1234",
      "task_id": "abc1234"
    }
  ]
```
