A **per-task** sandbox, mounted via MCP: a container (Docker) or a pod (Kubernetes) where you run builds, tests and dev servers without dirtying your own container. It outlives your run: if you come back to the same task, you find the processes you left running. Subtasks share their parent task's sandbox.

Your repos are in `/workspace/projects/<repo>/`, the same code you edit. Work there, don't copy.

This manual is the reference for the tools. How to discover how a project starts, write a good startup script, what survives when the sandbox shuts down, the limits and what to do on each failure are in the `octopus-sandbox` skill: read it before writing or fixing a script.

## First `init_status`, then `init_run`

Each project can have a **startup script** in Thulu: the bash that brings it up in this sandbox, with a manifest (parameters, ports, ready command, workdir). It exists so you don't waste time installing and guessing how each project starts.

1. **Before installing anything, call `init_status`.** It tells you, per project of the task, whether it has a script (`has_script`), its revision, parameters with their defaults, ports, and how the last run in this sandbox ended (`never`, `running`, `ready`, `failed`, `timeout`).
2. **If it has a script, use `init_run`.** Don't install dependencies or try commands by hand. `init_run` waits until the project is ready and returns `state`, `elapsed_ms`, `ports` and the last ~60 lines of the log.
3. **If it fails because of an incompatibility** (node version, a package that changed, a new variable), read the log, fix the script with `init_script_update` and a short note (`"pin node 20: pnpm 9 needs it"`), and go back to `init_run`. The change takes effect immediately and stays in the history; you don't need to ask permission.
4. **If it has no script and you managed to bring the project up by hand**, save what worked with `init_script_update`: the full script and the manifest. The next task starts from there.
5. **The script lives in Thulu, not in the repo.** Never add or commit sandbox scripts to the project's repo: whatever the script needs that the repo doesn't have goes in the manifest's `files`.

A `ready` from `init_run` with `ports: [3000]` means the app is already listening on 3000: ask for `preview_url` for that port and get on with your work.

## Install once: `setup`, `tools` and `cache`

If the script installs dependencies (`npm ci`, `pip install`, `go mod download`), take that part out of the script and put it in the manifest:

- `setup`: the bash that installs. `init_run` runs it before the script and skips it when it already ran with the same inputs.
- `cache.key`: the files that decide what gets installed, relative to the workdir (`package-lock.json`, `go.sum`, `poetry.lock`). If one changes, setup runs again.
- `cache.paths`: what setup leaves in the repo (`node_modules`, `.venv`). These are what gets saved and restored.
- `tools`: toolchain versions, `{ "node": "22", "python": "3.12" }`. `mise` installs them before setup and the script runs with them on the `PATH`. Don't download node, go or python with `curl` in the script. A `.tool-versions` or `mise.toml` in the workdir is also respected.

With that, `init_run` does one of four things and says which in `setup.status`:

- `skipped`: this checkout already has the install for this key.
- `restored`: it unpacked the default branch's snapshot instead of installing (seconds instead of minutes).
- `ran`: it ran setup. If it also says `no_cache: true`, this sandbox has no dependency cache and setup will run on every `init_run`.
- `failed`: setup or the tools install failed; the script didn't start. The log says why.

Snapshots only come from the default branch: Thulu runs setup for what is merged in a separate sandbox and saves it. What you install in your task is never shared with another one. If your task changes the lock file, the key changes and setup runs in full in your sandbox; that is expected. The new snapshot shows up when your change reaches the default branch.

## Tools

### `init_status`

**Args**: `project` (optional; name, directory or id). Without `project`, it reports every project of the task.

**Response**: `{ "projects": [ { "project", "project_id", "dir", "mounted", "has_script", "revision", "params", "ports", "ready_command", "workdir", "run": { "state", "revision", "started_at", "finished_at", "log_path", "exit_code" } } ] }`

`mounted: false` means the project has no worktree in this sandbox yet: its script can be read and edited, but not run.

### `init_run`

Copies the script to `/tmp/octopus-init/<dir>/run.sh` and starts it detached (`setsid`) from `/workspace/projects/<dir>/<workdir>`. If there was an earlier run of the same project, it first stops it entirely (its whole process group, including any dev server it left behind).

**Args**

- `project` (string): optional when the task has a single project.
- `params` (object): values for the manifest's parameters, by name. A name the manifest doesn't declare is rejected; the ones you don't pass take their default; a `required` one with no default must be passed. They reach the script as environment variables, along with Thulu's own (see below).
- `wait` (boolean, default `true`): waits until it's ready. It checks the `ready` command every 2 s until it exits with 0 or `ready.timeout_seconds` expires (default 300, maximum 900). With `false` it returns right away with `state: "running"`; check later with `init_status`.

**Script environment variables** (`ready` and `setup` see them too):

- `OCTOPUS_PROJECT`: the project's name.
- `OCTOPUS_TASK_ID`: the task.
- `OCTOPUS_INIT_FILES`: the folder with the manifest's `files`.
- `OCTOPUS_PREVIEW_URL_<port>`: one for each port the sandbox publishes (`OCTOPUS_PREVIEW_URL_8000`, `OCTOPUS_PREVIEW_URL_5173`...), with the same `url` that `preview_url` returns for that port: the address the task's other containers (the QA browser) use to reach the sandbox. A frontend that bakes in the API URL at build time takes it from here: `PUBLIC_API_URL="${API_URL:-$OCTOPUS_PREVIEW_URL_8000}"`. If it couldn't be computed for some port, that variable is absent. `setup` must not use them: the prewarm runs it with no task and without these variables.

**Response**: `{ "project", "state", "phase", "revision", "elapsed_ms", "ports", "log_path", "log_tail", "exit_code", "message", "setup" }`

`setup` shows up when the manifest has `setup` or `tools`: `{ "status", "key", "snapshot", "tools", "tools_ms", "restore_ms", "setup_ms", "exit_code", "no_cache" }`. `phase` is `tools`, `setup` or `start` while it runs. `ready.timeout_seconds` starts counting when the script starts, not during the install (which has its own 30-minute cap).

- `ready`: the `ready` command exited with 0, or the script finished with 0 and there is no `ready` command.
- `failed`: the script exited with a nonzero code (`exit_code`), or died without exiting. If setup failed, `setup.status` is `failed` and `exit_code` is setup's.
- `timeout`: the wait expired without `ready` returning 0. The process keeps running; look at the full log with `exec` (`tail -n 200 <log_path>`).

### `init_script_get`

**Args**: `project`. Returns `script`, `manifest`, `revision`, `author_label`, `note` and `updated_at`. Read it before editing it.

### `init_script_update`

Saves a new revision: the **full** script and manifest, not a diff. You sign it as this agent run (you can't choose the author). You can only edit your task's projects.

**Args**

- `project` (string)
- `script` (string, required): the full bash, up to 64 KiB. It runs with `bash` from the workdir. A long-running server can stay in the foreground as the last step or go to the background (`nohup ... &`); both work.
- `manifest` (object):
  - `params`: `[{ "name": "DB_URL", "description": "...", "default": "...", "required": false }]`. Names `^[A-Z][A-Z0-9_]*$`; `OCTOPUS_*` is reserved. Never put a sandbox's address in a `default`: it belongs to another task. For the sandbox's own URL use `${API_URL:-$OCTOPUS_PREVIEW_URL_8000}` in the script.
  - `ports`: `[3000]`, where the app listens. It's informational: it doesn't open or map anything.
  - `ready`: `{ "command": "curl -fsS http://localhost:3000/health", "timeout_seconds": 300 }`. Runs in the sandbox from the workdir, with the params in the environment; exit 0 = ready.
  - `workdir`: relative to the repo root, default `"."`.
  - `setup` (string): the bash that installs dependencies, up to 64 KiB. See "Install once".
  - `tools` (object): `{ "node": "22", "go": "1.25.1", "npm:typescript": "5.6.2" }`, up to 16. Names and versions as `mise` accepts them.
  - `cache`: `{ "key": ["package-lock.json"], "paths": ["node_modules"] }`, paths relative to the workdir, no `..` or `.git`. Without `paths` there is no snapshot to save.
  - `files` (object): support files, `{ "seed.mjs": "<text>" }`, up to 32, 64 KiB each and 256 KiB in total. They are stored in Thulu with the revision and written to `$OCTOPUS_INIT_FILES/<path>` before each run, outside the repo; setup, the script and `ready` read them from there. Changing one reruns setup.
- `note` (string, required): one line saying what changed and why. It shows in the project's history.

### `exec`

Runs a command with `bash -lc`. Each call is a new shell: `cd` and variables don't survive between calls.

**Args**

- `command` (string, required)
- `cwd` (string): default `/workspace`.
- `timeout_ms` (integer): the operator sets a maximum (`sandbox.max_exec_timeout_ms`, 30 min by default); asking for more is trimmed to that maximum. When it expires, the process is killed and `timed_out` is `true`.
- `env` (object): extra variables for that command.

**Response**: `{ "stdout", "stderr", "exit_code", "duration_ms", "timed_out", "timeout_ms" }`. `stdout` and `stderr` are cut at ~100 KB each: for long output redirect to a file and read it with `tail`.

### `preview_url`

Gives the URL for a port your app listens on inside the sandbox.

**Args**: `port` (integer, required). Only the ports the sandbox publishes work: **3000, 5173, 8000 and 8080** by default (the tool's description lists the current ones). If the port isn't published, the response is an error with the list of available ones; if your stack uses another, start it on one of those (`--port 5173`).

**Response**: `{ "url", "human_url", "port" }`

- `url` is the address for other tools that run in their own containers, like `thulu-browser`. On the agents network it's the sandbox container's name (`http://<sandbox>:<port>`); otherwise, `host.docker.internal` with the port published on the host.
- `human_url` is for a person in their browser, with the public host the operator configured.

- On Kubernetes, `url` is the sandbox's Service inside the cluster (`http://sandbox-<task>.<ns>.svc:<port>`) and only this task's pods can open it (your thulu-browser included). `human_url` comes back empty and `note` explains why. The first request may be rejected for a few seconds while the network policy is applied: retry.

To check from inside that the server responds, you don't need `preview_url`: `exec` with `curl -fsS http://localhost:<port>/`.

### `attach_file`

Attaches a file from the sandbox to the task: a PDF you generated, a report, a build. The server reads the bytes straight from the sandbox and they arrive whole.

**Args**: `path` (string, required), `filename` (default: the file's name), `mime_type` (default: by extension). Maximum 50 MB.

Never encode a file in base64 to paste it into a tool: that corrupts binaries. For browser screenshots use `thulu-browser`'s `screenshot` with `task_id`.

## Operating rules

1. **Background processes** with `exec` (when there is no script): `setsid nohup <cmd> > /tmp/<name>.log 2>&1 < /dev/null &`. Without `setsid nohup`, the process dies when `exec` returns.
2. **Before relaunching, look at what's there**: `init_status` shows whether there is already a `ready` run in this sandbox. If there is, don't repeat it unless you changed something that requires it.
3. **Startup logs**: `/tmp/octopus-init/<dir>.log` has the full output of the last `init_run`.
