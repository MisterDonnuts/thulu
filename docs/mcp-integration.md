# Adding MCP servers to Thulu

A cross-cutting guide to connecting a new MCP (Model Context Protocol) server
to Thulu agents. It covers the three deploy patterns the repo uses and how
the daemon wires them to each agent.

- Which bundled MCP servers get installed and how `thulu init` picks them:
  [`enterprise-setup.md`](enterprise-setup.md).
- Cluster runbook: [`mcp-server-deploy.md`](mcp-server-deploy.md).
- `thulu-figma`, reading Figma with a token from the credential manager:
  [`thulu-figma.md`](thulu-figma.md).

## What an MCP server is in Thulu

An MCP server is a long-running process that exposes **tools** over HTTP/SSE.
The agent consumes it through `--mcp-config`, and each tool becomes available
inside the prompt as `mcp__<server>__<tool>` (e.g.
`mcp__thulu-browser__screenshot`).

How it differs from the other layers:

| Layer | What it is | When |
|---|---|---|
| Skill | Knowledge as text (Markdown), a row of the workspace catalog ([`skills-authoring.md`](skills-authoring.md)) | Methodology, checklists, recipes |
| Project hook (`hooks/<stage>.sh`) | A script that runs in the worktree before the agent; its output goes into the prompt | A single project, no heavy deps |
| **`tools/` (MCP)** | A service with typed tools | Heavy deps, state/lifecycle, or use from several agents |

## How an agent ends up seeing the tool (the wiring)

With the MCP proxy ([`mcp-proxy.md`](mcp-proxy.md)) the agent's container
sees no server URL and no credential. It gets a list, `OCTOPUS_MCP_SERVERS`,
which the daemon builds from:

1. **`MCP_TOOLS`**: a CSV with the servers enabled for **this stage**. The
   dispatcher sets it from the MCP servers the agent type wires on that stage
   (`mcp_by_stage`, the agent's MCP tab in `/agents/`). Optional syntax:
   `name@scope` for servers that filter tools by scope (e.g.
   `thulu-github@write`) and `name@tool+tool` to grant individual tools.
2. **The forwarding table**: URL, transport and credential of each server,
   keyed by `MCP_<NAME>_URL` (e.g. `thulu-github` → `MCP_THULU_GITHUB_URL`).
   It is filled from the daemon's env (`collectMCPURLs(os.Environ())`) and from
   the `/mcps` registrations. In compose, `thulu init` writes the URLs into
   `deploy/.env`; in Helm the chart builds them from `mcp.<server>.enabled`.

Each granted server the table knows goes into the list as
`<OCTOPUS_API>/mcp-proxy/<name>` with the run's key. The exceptions are
`thulu-sandbox` (served by the daemon at `/sandbox/sse`) and per-run
`thulu-browser` (it listens on the agent's loopback). The daemon stores what
was granted in `mcp_run_grants` and the proxy checks it again on every request.

The suffix after `@` is one of two things, never both: a scope (`read`,
`write`) or a list of tools. The list of tools is separated by `+`, not by a
comma. `MCP_TOOLS` is a CSV, and so is the `thulu.mcp.default.<stage>` label of
the agent import format, so a comma inside an entry gets split into phantom
servers on the next read. The `+` becomes a comma only when the query string is
built. An explicit list of tools wins over the scope and grants those tools
even if they write (naming a tool is a deliberate grant). `thulu-github` also
accepts a scope inside the list: `thulu-github@write+run_dispatch` is
everything `write` grants plus `run_dispatch`. With no suffix, `thulu-github`
defaults to `write` and `octopus-api` to `read`.

```
 daemon                               pod env (dispatcher / chat)
 ──────                               ───────────────────────────
 table: thulu-github → http://…/sse   MCP_TOOLS=thulu-github@write,jira
        jira → https://… (http, cred) OCTOPUS_MCP_SERVERS=[
 mcp_run_grants ◀── recorded ───────    {thulu-github, …/mcp-proxy/thulu-github, sse},
                                        {jira, …/mcp-proxy/jira, http}]
                                      OCTOPUS_API_KEY=<the run's key>
                                        │
                                        ▼  mcp-config.sh / engines/codex.sh
                                      .mcp.json (Claude) · -c mcp_servers.* (Codex)
```

**Operating rule**: exposing a new MCP server = **setting an `MCP_*_URL` in
the daemon's env** (`deploy/.env` or the chart's `extraEnv`) **or registering
it from the `/mcps` UI** (persisted in `mcp_registrations`, seeded into the
dispatcher at boot and live through `Dispatcher.SetMCPURL`). No recompiling and
no code changes.

REST endpoints (admin):

| Method | Path | Effect |
|---|---|---|
| `GET` | `/mcps` | Merged list: env (`source:"env"`, `status:"configured"`), runtime (`source:"runtime"`) and registered (`source:"registered"`, `status:"external"`) |
| `POST` | `/mcps/register` | `{name, url, description?, forwards_credential?, transport?, auth?, credential_ref?, header_template?}`: persists it and updates the forwarding table |
| `PATCH` | `/mcps/{name}` | Edits any of those fields of a registered server (except `name`) |
| `POST` | `/mcps/probe` | `{url}`: which transport answers and whether it asks for OAuth ([`mcp-proxy.md`](mcp-proxy.md#probe)) |
| `DELETE` | `/mcps/{name}` | Deletes the registration; if the daemon's env has that URL, it goes back to it, otherwise it leaves the forwarding table |

If a name shows up in several sources, registered wins over runtime and runtime
over env. Runtime wins over env because it is the same server seen from inside
(on K8s the env URL points at the Service of those pods): the row keeps the
container's state and takes the env URL. The registration wins because it is
what the dispatcher forwards to the agents. For env and registered rows with
no container, `health` comes from opening the SSE URL and waiting for the
`endpoint` event (3 s timeout, 10 s cache): `healthy` only if it answered,
`unhealthy` with the reason in `health_detail` if not. Streamable HTTP
registrations and those with `auth: credential` stay unchecked: that probe
speaks SSE without credentials.

## Sessions: the run's key opens and signs each one

The bundled servers (`octopus-api`, `thulu-github`, `thulu-browser`,
`thulu-cloud`, `thulu-figma`) use the classic SSE transport: `GET /sse` opens
the session and each call is a `POST /messages?sessionId=<id>`. The rules:

- `GET /sse` without the run's key (`X-API-Key` or `Authorization: Bearer`)
  answers 401. The only exception is `GET /sse?catalog=1`, the probe the
  daemon uses to build the tool picker: it opens a session that lists tools
  and refuses every `tools/call`.
- Every `POST /messages` has to carry the same key as its session's handshake
  (constant-time comparison); otherwise 401. Knowing someone else's
  `sessionId` is not enough to talk in that session.
- `thulu-github` has no GitHub token of its own and does not read `GH_TOKEN` or
  `GITHUB_TOKEN`: a project with no GitHub connection fails with an error that
  says so, instead of acting with a token the operator left there.
- `thulu-browser` with `MCP_REQUIRED_KEY` accepts only that key, catalog probe
  included. The per-run mode (below) uses it.
- `thulu-browser` with `MCP_CATALOG_ONLY=1` opens only the catalog session (403
  to any other, with or without a key) and never runs a tool. It is the shared
  server of an install in per-run mode.

For the server to get the key, the registration needs `forwards_credential`
(or the name in `OCTOPUS_MCP_FORWARD_CREDENTIAL`): the proxy adds it to the
handshake and to every message. The agent sends it only to the proxy. The
daemon's probe (`/tools`, `/mcps`) counts a 401 as "server up": answering
already proves it listens.

Tests written as attacks: `tools/*/server.test.mjs` and
`tools/thulu-browser/test/session-auth.test.js`.

## Per-run thulu-browser

`browser_run_script` executes the agent's JavaScript inside the server's Node
process. In a shared thulu-browser, one run's script could replace
`globalThis.fetch` and read the keys other sessions send through that same
process. `OCTOPUS_BROWSER_MODE` decides where it runs:

| Mode | Default when | Where it runs |
|---|---|---|
| `shared` | no `OCTOPUS_SAAS_MODE` | the compose server or the chart's Deployment, at `MCP_THULU_BROWSER_URL` |
| `per-run` | `OCTOPUS_SAAS_MODE=1` | one per run that has `thulu-browser` on the stage, inside the agent's isolation boundary |

In `per-run`:

- The dispatcher points `MCP_THULU_BROWSER_URL` at `http://127.0.0.1:3010/sse`
  (`OCTOPUS_BROWSER_PORT`), marks the env with `OCTOPUS_BROWSER_PER_RUN=1` and
  lists it in `OCTOPUS_MCP_SERVERS` directly (not through the proxy: the daemon
  cannot reach the agent's loopback) with the run's key. The forwarder
  withholds the shared URL (`Forwarder.Withhold`): no container gets it and
  the proxy does not serve it either, even if it is in the daemon's env or
  registered in `/mcps`.
- **Kubernetes**: the agent's pod gets a native `thulu-browser` sidecar (an
  initContainer with `restartPolicy: Always`, after the clones). The kubelet
  starts it before the agent, restarts it if Chromium takes it down and stops
  it when the agent finishes, so the pod ends with the run; the exit code is
  still the agent's. It listens on `127.0.0.1` and gets `OCTOPUS_API` and the
  run's key (as `MCP_REQUIRED_KEY`) from the pod's own Secret, and nothing else
  from the env. It meets Pod Security `restricted` in its own container:
  non-root (uid 1000), drop ALL, no escalation, seccomp `RuntimeDefault`,
  read-only root filesystem, with bounded `emptyDir`s for `/tmp`,
  `/agents-shared` and `/dev/shm` (in memory). Chromium still runs with
  `--no-sandbox`: the boundary is the pod's RuntimeClass (Kata).
  Requests/limits: `OCTOPUS_BROWSER_CPU_REQUEST`/`_LIMIT` (500m/1) and
  `OCTOPUS_BROWSER_MEMORY_REQUEST`/`_LIMIT` (1Gi/1536Mi); the daemon does not
  start if the limit exceeds the tenant's per-container maximum.
- If the stage grants only some browser tools
  (`thulu-browser@screenshot+get_text`), the launcher passes them to the server
  as `MCP_ALLOWED_TOOLS`. The `?tools=` the agent writes in its `.mcp.json` can
  shrink that list, never grow it: an agent that edits its config does not
  reach `browser_run_script` if the stage did not grant it.
- A run that started without a key (it could not be issued) gets no browser:
  it would have nothing to open a session with, and on Kubernetes the
  reference to the Secret without that key would leave the pod in
  `CreateContainerConfigError`.
- **Docker**: the launcher pulls the browser image before starting the agent
  (the first pull takes minutes). As soon as the agent starts, it creates
  `octopus-browser-<agent id>` with `NetworkMode: container:<id>` (it shares
  the agent's loopback, network, `/etc/hosts` and DNS), read-only root
  filesystem, bounded tmpfs, 1 GB `shm`, drop ALL, `no-new-privileges`, uid
  1000 and the memory, CPU and pids limits. Labels: `thulu.browser=true`,
  `thulu.browser.agent=<id>`, `thulu.task`, `thulu.workspace_id`,
  `thulu.instance` (never `thulu.agent`, so the watcher does not take it for a
  run). If it does not start, the launch fails and the agent is deleted. It is
  deleted when the agent exits (always, with or without
  `agent.reap_on_exit`), by `teardownTask` through the task label, and the
  janitor deletes the ones left with no live agent. `thulu gc` classifies
  them as `browser`.
- The agent waits up to 60 s for the browser's `/health` before writing
  `.mcp.json` (`images/snippets/mcp-wait.js`); after that it goes on and only
  the browser tools fail.
- The browser reaches what the agent reaches: the daemon at `OCTOPUS_API` and
  the sandbox's preview URL (`PreviewInfo.URL`). In Docker that URL is the
  sandbox container's name on the agents network, which the agent's DNS
  resolves, or `host.docker.internal` from the agent's `/etc/hosts` on the
  legacy bridge; the browser shares both.
- The shared server still exists for the daemon's tool picker, but runs
  nothing: the chart sets `MCP_CATALOG_ONLY=1` on it when the effective mode is
  `per-run` (`extraEnv.OCTOPUS_BROWSER_MODE`, or `extraEnv.OCTOPUS_SAAS_MODE`
  with no explicit mode) and passes the daemon `OCTOPUS_BROWSER_IMAGE` with the
  chart's image (it honors `mcp.imageRegistry`). In compose, set it by hand on
  the `thulu-browser` service if you use `per-run`.
- Screenshots, PDFs and videos still go through the API: the browser uploads
  them with the run's key. ffmpeg comes in the image and writes to `/tmp`.
- The image comes from `OCTOPUS_BROWSER_IMAGE`, else from
  `OCTOPUS_MCP_BROWSER_IMAGE`, else from
  `<OCTOPUS_IMAGE_REGISTRY or ghcr.io/misterdonnuts>/octopus-mcp-browser:<OCTOPUS_VERSION>`.
  The janitor keeps it (`dockergc.KeepFromEnv`).

What the per-run mode does not cover: chats have no browser (the shared
URL is withheld from them and they have no sidecar), and the browser does not
inherit the corporate `HTTP(S)_PROXY`.

## The three deploy patterns

Pick by **where the state the MCP server needs lives**:

| Pattern | When | Examples |
|---|---|---|
| **Docker (compose)** | An install with `thulu init` on Docker, or dev | profiles in `deploy/docker-compose.yml`; `tools/docker-compose.yml` for dev |
| **In-cluster K8s** | Production, stateless server | `deploy/helm/octopus/templates/mcp.yaml` (`mcp.<server>.enabled`) |
| **External** | The server already runs elsewhere: another cluster, a service of yours, a machine with its own state | registered in `/mcps` with its URL |

All three use the **same mechanism** in the agent (`MCP_TOOLS` +
`MCP_<NAME>_URL`); only where the process runs and which URL points at it
change.

## Generic recipe

Steps common to the three patterns; the specific ones are below.

### 1. Create the server (`tools/<name>/`)

Layout:

```
tools/<name>/
├── server.js       # SSE MCP, exposes typed tools
├── package.json    # the server's deps (NOT the agent's)
├── tool.yaml       # declarative catalog: name, description, tools, scopes
├── skill.md        # optional: manual pasted into the prompt when the MCP is wired
└── README.md       # how to run it, env, caveats
```

Conventions every server follows:

- **Never** a global `express.json()`: it breaks `SSEServerTransport`.
- Serialize calls if the server has a single resource (one browser, one
  connection, etc.).
- Read-only by default unless the design says otherwise: refuse mutations in
  the server, not in the prompt.
- Copy the session gate of the bundled servers (see [Sessions](#sessions-the-runs-key-opens-and-signs-each-one)):
  no key, no session, and every message carries the handshake's key. Never a
  fallback token of the server's own.
- A capability that must be deniable per agent is a separate tool, not a
  parameter of a tool that is already granted. The `@` gate filters by tool
  name, so an optional `board_id` inside `task_create` would be out of the
  permission's reach. Tools that are dangerous by default (deploys, merges) sit
  outside the read/write scopes and have to be named.
- If you take tools out of the scopes, implement `?catalog=1`: the picker
  probes with `scope=write`, and the catalog session widens only `tools/list`,
  never a tool call. Without it those tools cannot be ticked.
- A bundled server needs its line in `tools/embed.go` (`embed_test.go` fails
  without it), which embeds its `tool.yaml` and `skill.md` in the binary.

### 2. Register its use in the agent

In `/agents/`, on the MCP tab of the agent that will use it, tick the server on
the stage where it is used. Through the API it is the same field:

```bash
curl -X PUT -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"mcp_by_stage":{"in_progress":["ticket-reader","thulu-github@write"]}}' \
  http://<host>:8585/types/<agent>/settings
```

The dispatcher reads that list and sets `MCP_TOOLS` in the pod during the
stage.

Usage instructions that hold for any agent go in `tools/<name>/skill.md`: they
travel on their own to every run that wires the MCP server
(`images/snippets/mcp-skills.sh`). What belongs to one agent (when to call the
tool in its stage) goes in its `prompts/<stage>.md` prompt.

### 3. Deploy, per pattern

**A. Docker (compose)**

- Add the service to `tools/docker-compose.yml` (dev) or as a profile in
  `deploy/docker-compose.yml`, on the `agents` network and without publishing
  ports outside `127.0.0.1`.
- Add `MCP_<NAME>_URL=http://<service>:3000/sse` to `deploy/.env`: the daemon
  forwards it. Agents run on the `OCTOPUS_AGENT_NETWORK` network and resolve
  the service name.
- `docker compose -f tools/docker-compose.yml up -d --build`.

**B. In-cluster K8s**

See [`mcp-server-deploy.md`](mcp-server-deploy.md). A new bundled server is
added to the catalog in `templates/_mcp.tpl` and to `mcp:` in `values.yaml`;
the chart creates the Deployment and Service and passes the URL to the daemon.

**C. External**

When the server runs outside the stack:

1. Register the URL. Two equivalent ways:
   - **UI (recommended)**: in `/mcps`, **Add MCP** → name, URL
     (`http://my-mcp.internal:3000/sse`) and description. It is persisted in
     `mcp_registrations` and the dispatcher forwards it to the next agent
     without a restart.
   - **Daemon env**: `MCP_<NAME>_URL=…` in `deploy/.env` or in the chart's
     `extraEnv`. Needs a restart and leaves no record in the UI.
2. The URL has to resolve and be reachable **from the agents**, not from your
   browser.

Trade-offs of this pattern:

- SSE brings no TLS or auth of its own: expose the server only to the agents'
  network.
- Do not tick "Acts on Thulu for the agent" unless the server is yours: it
  hands it the run's key.

### 4. Verify

- **Service up**: `curl http://<url>/sse` hangs with the connection open (SSE);
  connection refused means it is not there.
- **Agent loads it**: the run's `session.log` should show
  `"mcp_servers":[{"name":"<name>","status":"connected"}]` and the tools
  listed as `mcp__<name>__*`.
- If you see `status:"failed"`: wrong URL, blocked network, or the server
  refused the handshake.

## Cross-cutting traps

These apply whatever the pattern (more in `mcp-server-deploy.md`):

1. **A global `express.json()` breaks SSE**: the transport reads the raw
   stream.
2. **`host.docker.internal` on Colima**: Docker Desktop injects it, Colima
   does not. Add `extra_hosts: ["host.docker.internal:host-gateway"]` to the
   compose of the tool and of the agent.
3. **Exact Playwright pin**: the Playwright base image preinstalls the browsers
   for **its** version. `"playwright": "1.60.0"` (no `^`).
4. **On K8s the forwarder picks up `MCP_<NAME>_URL` only if it is an env var
   of the daemon's process**: put it on the Deployment (chart or
   `kubectl set env`), not in a loose ConfigMap nobody mounts.
5. **Rebuilds without updating the pin in containerd**: `docker save | k3s ctr
   images import` **replaces** the layer, but if the image was already pinned
   with an old digest, the kubelet may keep seeing it. Re-pin after every
   import.
6. **Captures travel through the API, not through disk.** `screenshot` with
   `task_id` uploads the image to the task's attachments from the server;
   there is no volume shared between the MCP server and the agent in any mode.
7. **External: the network is still an auth surface.** The bundled servers ask
   for the run's key on every session (see "Sessions"), but a third-party MCP
   server need not: never expose it to the internet.
8. **An MCP server that calls Thulu has no credential of its own.** The
   run's key travels in the handshake's `X-API-Key` header and the server uses
   that one; who receives it is declared (`forwards_credential` in the
   registration, or `OCTOPUS_MCP_FORWARD_CREDENTIAL` in the daemon's env for
   those that come through `MCP_<NAME>_URL`). Naming a third-party MCP server
   there hands it a valid credential for the workspace of whichever agent
   calls it.

## When to create a new MCP server, or something else

- **A new, complex tool** (heavy deps, state, several consumers) → an MCP
  server in `tools/`.
- **A methodology or checklist** the agent must follow → a workspace skill in
  `/skills/`.
- **A trivial script for one project, no deps** → a project hook
  (`hooks/<stage>.sh`).
