# Working with the sandbox

The sandbox is the machine where your task's project runs: builds, tests,
dev servers, the app QA is going to look at. The `thulu-sandbox` manual has the
reference for each tool; this skill teaches how to use them well: start a
project fast, leave the next task a script that works and not lose
turns fighting the environment.

## What runs where

| | Your container | The sandbox |
|---|---|---|
| What for | reading and editing code, git, your tools | installing, building, testing, running servers |
| Dependency and toolchain cache (`/cache`) | no | yes |
| Visible to the QA browser | no | yes, through `preview_url` |
| Lives | as long as your run | as long as the task, across runs |

Both see the same code at `/workspace/projects/<repo>/`: what you edit in
your container is already in the sandbox, with nothing to copy. Whatever you install in your
container doesn't use the cache and is lost when the run ends, and nobody can reach an app
you start there. Install, build and run in the sandbox.

Git runs from your container, as `octopus-run` says: on Kubernetes with git in your
terminal, on Docker with `thulu-github`. The sandbox has no GitHub credentials
and doesn't need them.

Subtasks share their parent task's sandbox, and if you come back to the same
task you find the processes you left running.

## What survives

Thulu shuts the sandbox down after 30 minutes idle
(`sandbox.idle_evict_seconds`) and recreates it when someone asks for it. When it
is recreated:

- **Kept:** `/workspace/projects` (the code and whatever the install left
  in the repo, like `node_modules`) and `/cache` (the npm, Go and pip caches and the
  mise toolchains, shared by the whole workspace).
- **Lost:** everything else (the processes, `/tmp`, `$HOME` and anything
  installed outside those two paths).

That is why whatever starts the project goes in the startup script, never in a
sequence of `exec` calls only you know about. If `init_status` says `never` on a
project you had already started, the sandbox was recreated: run `init_run`
again.

## Starting a project

1. `init_status`. If the project has a script, `init_run` and you are done; if the
   previous run is still `ready`, not even that.
2. With no script, find out how it starts before trying commands:
   - the README and the development docs;
   - `package.json` (`scripts`), `Makefile`, `justfile`, `Procfile`;
   - `docker-compose.yml`: which services the app needs, with which ports and
     variables;
   - the CI (`.github/workflows/`): that is where the install and test steps that
     actually work are, with each toolchain's version;
   - `.tool-versions`, `mise.toml`, `.nvmrc`, `go.mod` (`go 1.25`),
     `pyproject.toml`: the versions.
3. Start it by hand with `exec` until it responds, and save what worked with
   `init_script_update` in the format from the next section. Files you
   put together for testing go to `/tmp` in the meantime and later to `files`, never to the
   repo. If something can't be
   worked out from the repo (a variable with no example value, an external service),
   ask with `task_ask` instead of making up a value.

## Writing the startup script

The startup script belongs to Thulu, not to the project: it is stored in Thulu with
its history and the sandbox takes it from there. The repo belongs to the project and is not touched
to start the sandbox.

- **Never add or commit sandbox scripts to the repo** (`up.sh`,
  `install.sh`, a `package.json` for a tool, a seed). Whatever the script
  needs that the repo doesn't have goes in the manifest's `files`: Thulu stores it with
  the revision and writes it to `$OCTOPUS_INIT_FILES/<path>`, outside the repo, before
  each run.
- **The script uses what the repo already has** for its developers: the
  `package.json` `scripts`, the `Makefile`, the code. If starting it requires
  changing something in the project, that is a task's work and goes in its PR with
  its own justification, not hidden in the script.
- **A tool the project doesn't use** (a database emulator, a
  static server) is declared in `tools` with the mise backend
  (`"npm:serve": "14.2.4"`), not with a new `package.json` in the repo.

Split the work across five places:

| Goes in | What | Example |
|---|---|---|
| `tools` | toolchain and tool versions | `{"node": "22", "go": "1.25.1", "npm:serve": "14.2.4"}` |
| `setup` | installing dependencies, nothing else | `npm ci && go mod download` |
| `cache` | what decides the install and what it leaves behind | `{"key": ["package-lock.json", "go.sum"], "paths": ["node_modules"]}` |
| `files` | support files the repo doesn't have | `{"seed.mjs": "...", "static.mjs": "..."}` |
| `script` | starting what is already installed | local migrations, servers |

Rules:

- **No `curl` to install node, go or python.** Declare them in `tools`:
  mise downloads them once per workspace to `/cache/mise`. If the repo already has
  `.tool-versions` or `mise.toml` in the workdir, those work too.
- **`setup` only installs.** No servers, no migrations against a database the
  script starts, no depending on `params`: the prewarm runs it
  without a task, on the default branch, and without `.git` (a setup that calls git
  fails there).
- **`cache.key` lists every lock file** that changes the install, including
  those in subfolders (`web/package-lock.json`). If one is missing, a task can
  restore stale dependencies.
- **`cache.paths` lists everything the install leaves in the repo**
  (`node_modules`, `web/node_modules`, `.venv`). Anything not listed there is reinstalled
  on every checkout. Package caches (`~/.npm`, Go modules) already live in
  `/cache`: they don't go in `paths`.
- **The script starts by tearing down the previous run** if the project has a
  `down` (`bash scripts/down.sh || true`). `init_run` already kills the previous
  run's process group, but not what that run left outside it (a
  pidfile, a port held by a child that detached).
- **`ready` checks what someone is going to use**: the API's health endpoint and
  the front end's page, with `curl -fsS`. A `sleep 10` is not a check.
  `timeout_seconds` counts from when the script starts, not during the
  install; 300 is enough for most projects.
- **Ports**: the app has to listen on one of the ports the sandbox publishes
  (3000, 5173, 8000 and 8080 by default; the `preview_url` description lists
  the current ones). If the project uses another one, change it with a variable or flag in the
  script (`--port 5173`), not in the project's code.
- **Whatever changes between environments goes in `params`**: the public API URL the
  front end uses, a port, a mode. On Kubernetes the QA browser runs in another
  pod and can't reach the sandbox's `localhost`: the URL the front end hands the
  browser has to be the `url` from `preview_url`. `init_run` already exports it,
  one per published port, as `OCTOPUS_PREVIEW_URL_<port>`. In the script,
  use that variable when the parameter comes in empty
  (`PUBLIC_API_URL="${API_URL:-$OCTOPUS_PREVIEW_URL_8000}"`) and you don't need to
  pass it to `init_run`. **Never put a sandbox's address in a parameter's
  `default`** (`http://sandbox-….svc:8000`): it is another task's sandbox, and the
  front end gets built against it without anything failing.
- **The revision note says what changed and why**, in one line: "setup and
  tools instead of curl for Go; node_modules cached".

### Checking the phases

`init_run` twice in a row: the first has to report `setup.status` `ran` and the
second `skipped`, with `ready` in both. If the second installs again, something
is missing from `cache.paths` or the setup writes to a path in `cache.key`.

On save, Thulu prewarms the default branch: the next task should see `restored`.

## Sandbox limits

- **Root depends on the runtime.** Check `id -u` before installing a system package
  (`libvips`, a Postgres client):
  - `1000`: Kubernetes, or Docker with `OCTOPUS_ROOTLESS_AGENTS`. No root and no
    `apt`. Look for an alternative without root (a binary mise can install, an
    npm or pip package that bundles it). If there is none, say so in the task with what
    is missing and stop trying.
  - `0`: Docker by default. `apt-get install` works, but what you install lives
    in the container and is lost when the sandbox shuts down. Put it in the
    startup script's `tools` or `setup` phase so it repeats, and still prefer
    the alternative without root: the same script has to work on Kubernetes.
- **No Docker inside.** A `docker-compose up` doesn't work. The services
  the app needs run as processes in the sandbox (an emulator instead of
  the managed service, sqlite, the project's local or in-memory mode) and the script starts them. If the project only runs with compose and has
  no local mode, say so in the task.
- **Network.** On Kubernetes egress is over HTTPS (443) to public addresses, or
  only through the operator's proxy: a registry on another port, a database on the
  company's internal network or a service over SSH won't respond. Use each
  package manager's HTTPS mirrors. On Docker there is no filter.
- **Memory.** 6 GB by default. A process that goes over the limit dies with code
  137; if it is the build, lower the parallelism (`--max-workers=2`,
  `NODE_OPTIONS=--max-old-space-size=3072`, `GOMAXPROCS=2`) before reporting.
- **Time.** Each `exec` has a cap (30 minutes by default) and so does the
  `init_run` install. Anything that takes longer goes in the background with
  `setsid nohup ... &` and you follow it with `tail` on the log.

## When something fails

| Symptom | What to look at | What to do |
|---|---|---|
| `init_run` gives `timeout` | the full log (`tail -n 200 <log_path>`) and whether the port is listening (`curl -fsS localhost:<port>`) | if the server started and `ready` checks something else, fix `ready`; if it is still starting, raise `timeout_seconds`; if it died, the log says why |
| `setup.status: failed` | the setup log | toolchain version (`tools`), a broken lock file, network; fix it and run `init_run` again |
| `failed` with code 137 | memory | lower the parallelism, see Limits |
| `preview_url` responds, the browser doesn't load | on Kubernetes, the first seconds of a new sandbox | retry; if it persists, the front end is requesting `localhost` or another task's sandbox: make the script take the URL from `$OCTOPUS_PREVIEW_URL_<port>` |
| the port is not published | the `preview_url` response lists the current ones | move the app to one of those from the script |
| `init_status` says `never` on something you already ran | the sandbox was recreated | `init_run` again |
| `mounted: false` | the project doesn't have a worktree in this sandbox yet | you can read and edit the script, not run it; wait for the task to leave the initial stage |

If after two attempts the problem is the environment and not the project (a
system package is missing, a host doesn't respond, the quota won't let the
sandbox be created), stop fighting it: explain in the task what is missing, with the error, and
carry on with whatever doesn't need it.

## For QA and for the next stage

- Leave the app running when you finish: the next stage finds the sandbox
  as you left it as long as 30 minutes haven't passed.
- For the browser, the `url` from `preview_url`. For a person, `human_url` (on
  Kubernetes it comes back empty: say in the report how to start it, not a link).
- A file you generated (a PDF, a coverage report, a build) goes to the
  task with `attach_file`, never pasted as base64.
- In the report, if you started the project: which script revision ran, what
  `setup.status` said and how long each phase took. If you had to fix the
  script, the revision note.
