# Setting up Thulu

`thulu init` installs Thulu on Docker or on Kubernetes. It asks the
questions below, writes `deploy/.env` (Docker) or `deploy/values-<release>.yaml`
(Kubernetes), creates the first workspace and its operator key, and starts the
stack. Rerunning it is safe: every answer defaults to what the install already
has, and non-secret answers are kept in `deploy/.octopus-init.json`.

Nothing is cloned. The binary carries the `deploy/` tree and writes it out on
first run.

## Getting the binary

```bash
curl -fsSL https://github.com/MisterDonnuts/thulu/releases/latest/download/install.sh | sh
```

To build from source instead, see [development.md](development.md#run-it).

The script comes from the release it installs. It downloads
`octopus_<version>_<os>_<arch>.tar.gz` (linux and macOS, amd64 and arm64),
checks it against the release's checksums file, and checks that file's cosign
signature when cosign is installed. Then it asks whether to run `thulu init`;
without a terminal, or with `OCTOPUS_NO_INIT=1`, it prints the command instead.
It never uses sudo and never edits your shell's startup files: when the
directory it picked is not on your `PATH`, it prints the line to add.

`sh -s -- --version <version>` (or `OCTOPUS_VERSION=<version>`) installs a
specific release, `OCTOPUS_INSTALL_DIR` chooses where the binary lands
(default: a writable directory already on `PATH`, else `~/.local/bin`), and
`--help` lists the rest. Running it again for the release you already have
does nothing.

Run outside a checkout, `thulu init` puts the deployment files in
`~/.octopus` (`OCTOPUS_HOME` overrides it). Every install option runs the
published images, and init pulls only what the answers need: the daemon,
Postgres unless you bring your own, the agent image of each engine you chose,
the MCP servers you ticked, and Caddy only on its stack.

## The steps

Before the first question the wizard checks the machine: whether Docker
answers, the Compose v2 plugin, the CPU architecture against the published
images (linux/amd64 and linux/arm64), whether ports 8585 and 5433 are free, and the current
kubectl context. A failed check is a warning; it does not stop the setup.

In a terminal the questions are forms (arrow keys, space to tick, Enter to go
on) and each option says what picking it does. `thulu init -plain` asks in
numbered plain text instead, for screen readers and terminals that draw forms
badly; a pipe or `-non-interactive` gets the plain prompts too. While the
stack starts, each step is one line with a spinner, and what docker and helm
print goes to `deploy/init.log` (the previous run's stays as `init.log.1`);
a failing step shows the end of that log.

To run it unattended (CI, a provisioning script, no terminal), pass
`-non-interactive`: every question takes its default, so a first run is the
quick install and a rerun repeats what `deploy/.octopus-init.json` and
`deploy/.env` (or `deploy/values-<release>.yaml`) already say. Secrets are
never defaulted: the Git token and the engine keys stay skipped and go in
later on `/settings`. An answer the wizard cannot accept (a Caddy stack with
no domain, an OIDC issuer that fails discovery) stops the run with exit code 1
instead of asking again. Without the flag, a pipe has to carry every answer:
input that ends before the confirmation stops the run without writing
anything. `-dry-run` asks everything and shows the file changes and the
command, and writes nothing, not even the `deploy/` tree.

Every file the wizard writes (`deploy/.env`, the values file, the answers
file) is written 0600 through a temporary file and a rename, so a crash
leaves the old file or the new one. A rerun keeps the JWT secret, the
credential key, the Postgres password and the operator workspace it finds,
and a stack that fails to start can be fixed and the wizard rerun: it picks
up from the config already written.

1. **Mode.**
   - **Quick, on this machine** (the default on a first run): Docker with the
     published image (`docker-compose.standalone.yml`), no login, published on
     `127.0.0.1:8585`. It asks only steps 9 to 11; everything else takes its
     default and shows up in the summary, and what it never asked (login,
     license) is not listed as skipped. This first question is not numbered:
     the steps after it are, once the mode says how many there are.
   - **Custom Docker**: one node; agents are sibling containers launched over
     the Docker socket. Both stacks it offers run the published images:
     - **Server with a domain and HTTPS** (`docker-compose.caddy.yml`):
       daemon, Postgres and Caddy, which gets a certificate for your domain.
       Ports 80 and 443 have to reach the machine.
     - **Only on this machine, or behind your own proxy**
       (`docker-compose.standalone.yml`): nothing in front, port 8585 on
       `127.0.0.1` unless you give another address.

     An install that an earlier run set up on a stack that compiles from the
     checkout (`docker-compose.yml`, `docker-compose.dev.yml`) is offered
     "keep the current one", and Enter keeps it.
   - **Kubernetes / k3s**: Helm chart; agents are pods under a RuntimeClass
     and the daemon has no Docker socket.

   A rerun defaults to the mode the previous run chose. Both published stacks
   ask which image to run and default to the binary's own release.
   `thulu init -stack <compose file>` skips the mode and stack questions
   and runs that file on Docker; it is how a checkout gets the development
   stack (see [CONTRIBUTING.md](../CONTRIBUTING.md)).
2. **Identity.** OIDC, GitHub OAuth, or no login (development and standalone
   stacks only, published on loopback, and the default there). OIDC works with any provider: Okta,
   Microsoft Entra ID, Keycloak, Google, Auth0. It asks for the issuer URL,
   client ID and secret, button label, the email domains allowed to sign in
   (`OCTOPUS_SSO_ALLOWED_DOMAINS`; skipping it lets in anyone the IdP
   authenticates), and optionally a group claim and a group-to-role map.
   Thulu does not speak SAML. A SAML-only IdP (ADFS, Shibboleth) goes
   through a broker that speaks both, such as Keycloak or Entra ID, and the
   issuer is the broker.
3. **Database.** The bundled Postgres, or a DSN for an external one. For the
   bundled one the wizard generates `OCTOPUS_PG_PASSWORD`. Postgres is
   published on `127.0.0.1:5433` only, which is how the wizard reaches it to
   create the workspace. On a new install with 5433 taken, the wizard picks the
   next free port and writes it to `OCTOPUS_PG_HOST_PORT`.
4. **Network.** The public domain, allowed origins, and the host address to
   publish on for the standalone stack. A Docker install at `localhost` is
   asked only the domain: its origin is `http://localhost:8585` (a list an
   earlier run left is kept) and nothing stands in front of it. The domain has to be a bare host
   (`thulu.example.com`, no scheme, port or path) and each origin an
   `http://` or `https://` URL with a host and nothing after it; a wrong one
   is asked again, and stops a `-non-interactive` run. Which proxies may set
   `X-Forwarded-For` depends on the stack: the two with Caddy trust only
   it (nothing is asked), the dev stack trusts nobody, and the
   standalone stack and Kubernetes ask for your proxy's or ingress
   controller's addresses, with Enter meaning nobody.
5. **Kubernetes** (Kubernetes mode only). Namespace, release name, agent
   isolation (`kata-clh`, `gvisor` or none) and the agent image. A rerun
   defaults to the release it installed and reads every other answer back
   from its values file, the DSN and the license included.
6. **MCP servers.** A checklist of the tool servers agents call during a run.
   See below.
7. **License.** An enterprise license key, or skip for the community edition.
8. **Workspace.** The first workspace's name and owner email. The slug is
   derived from the name; a rerun that keeps the name keeps the slug the
   earlier run used. The wizard
   mints the operator API key and prints it once; only its hash is stored.
   The key expires after 90 days (see
   [rotating the operator key](#rotating-the-operator-key)).
9. **Engines.** Which of Claude Code, Codex and OpenCode this install uses,
   and a credential for each. Nothing is installed on this machine: each
   engine has its own agent image, the install (and later `thulu apply`)
   pulls the ones chosen here, and the daemon pulls another engine's on its
   first run. Each credential becomes that engine's default LLM profile,
   which agents use unless they name another.
   - Claude Code: an Anthropic API key (`sk-ant-api…`) or a Claude
     subscription token (`sk-ant-oat…`, from `claude setup-token`). The
     token is for the installation owner's own testing, and with agents in
     Kubernetes it needs `OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN=1` (see
     [workload-identity-federation.md](workload-identity-federation.md#claude-oauth-token-promax)).
   - Codex (see [engines.md](engines.md)): an OpenAI API key (`sk-…`).
   - OpenCode (see [opencode.md](opencode.md)): a key, the base URL of any OpenAI-compatible API (default
     `https://api.deepseek.com/v1`) and the model (default `deepseek-chat`).

   A key of the wrong shape is asked again. Every credential can be skipped
   and added later on `/settings`.
10. **Runner** (Docker only, optional). A runner is `thulu runner run` on
    this machine, taking agent runs from the install: the agent works with the
    machine's git, tools, local MCP servers and `claude` login, and the code
    of the runner's projects never reaches the daemon. Answering yes:
    - checks git, bash, curl, node and claude, and whether claude has a
      session (`claude auth status`); a missing tool is deferred with the
      command that fixes it;
    - offers to install the Claude Code version the agent image runs
      (`npm install -g @anthropic-ai/claude-code@<version>`) when claude is
      missing and npm is there;
    - asks for the folders to lend, each a git checkout that becomes a project
      of the runner;
    - asks whether to keep the runner running as a service.

    After the stack is up the wizard registers the machine in the first
    workspace (`~/.octopus/runner.json`), adds the folders as projects and
    installs the service. The runner runs Claude Code only; agents on another
    engine keep running in containers.
11. **GitHub.** A GitHub token for agents to clone and push with, then,
    outside the quick install, a GitHub Enterprise Server URL (Enter keeps
    github.com; the API URL is derived as `<url>/api/v3`). Both can be
    skipped; skipped items show up on the dashboard's onboarding checklist.

The summary lists every answer before anything is written. From there you can
install, look at the changes to `deploy/.env` and the command that will run,
or cancel without writing anything. A rerun that finds the workspace already
holds an operator key cannot read that key back (only its hash is stored), so
for the steps after boot it mints a key for that run alone and revokes it
before it exits.

Two secrets are generated, never asked for: `OCTOPUS_JWT_SECRET`, which signs
sessions, and `OCTOPUS_SECRETBOX_KEY`, which encrypts stored credentials. A
rerun keeps both (on Kubernetes it reads them back from
`deploy/values-<release>.yaml`); a new credential key would make every stored
Git token and LLM key unreadable.

No agents are preinstalled. After the install, create them on `/agents` in the
dashboard, or import your own definitions:

```bash
thulu agents import ./our-agents          # a directory of agent definitions
thulu agents import ./our-agents.json     # a pack from `thulu agents export`
```

### The runner service

On macOS the service is a launchd agent
(`~/Library/LaunchAgents/dev.octopus.runner.plist`); on Linux a systemd user
unit (`~/.config/systemd/user/octopus-runner.service`). Both start the runner
at login, restart it when it exits (at most twice a minute), log to
`~/.octopus/runner.log` and run with the `PATH` the install had, so the runner
finds the same claude, git and node as your shell. On Linux, `loginctl
enable-linger` keeps it running without a session.

```bash
thulu runner service status      # installed? running?
thulu runner service install     # write it and start it (again)
thulu runner service uninstall   # stop it and remove it
```

The service points at the `thulu` binary that installed it, so install it
from an installed binary, not from `go run`.

## Choosing MCP servers

```
     [x] 1) octopus-api
            the board for agents: read and update tasks, comments, attachments; acts with the run's own key
     [x] 2) thulu-github
            GitHub: pull requests, commits, branches, Actions runs
     [x] 3) thulu-browser
            headless Chromium (Playwright): navigate, click, screenshots attached to the task
     [ ] 4) thulu-cloud
            read-only cloud metrics and logs (GCP, AWS); needs a cloud credential per environment
     [ ] 5) thulu-figma
            read-only Figma files, frames, renders and comments; needs a Figma token on /credentials
   MCP servers — numbers toggle, a=all, n=none, Enter confirms:
```

Everything is checked except `thulu-cloud`, which reads nothing until an
environment has a cloud credential, and `thulu-figma`, which reads nothing until
the workspace has a Figma credential ([`thulu-figma.md`](thulu-figma.md)). `thulu-sandbox` (commands and port previews
in the task's sandbox) is not in the list: the daemon serves it and it is always
on. On a rerun the checklist starts from what is installed.

What the choice writes:

| | Docker (`deploy/.env`) | Kubernetes (values file) |
|---|---|---|
| Which servers run | `COMPOSE_PROFILES=<names>`; each server is a compose profile | `mcp.<server>.enabled: true/false` |
| Record of the choice | `OCTOPUS_MCP_SERVERS=<names>`, or `none` | the same flags |
| URL agents get | `MCP_<NAME>_URL=http://<name>:3000/sse` | set by the chart: `http://<fullname>-<name>.<ns>.svc:3000/sse` |
| Servers that get the run's API key | `OCTOPUS_MCP_FORWARD_CREDENTIAL` | set by the chart |

Unticking a server removes its URL only if it still points at the bundled
service; a URL you pointed elsewhere by hand stays. Names in
`OCTOPUS_MCP_FORWARD_CREDENTIAL` that are not Thulu's own servers stay too.

Every MCP container runs as uid 1000 with a read-only root filesystem, no Linux
capabilities and no privilege escalation, and publishes no port on the host. On
Docker the servers, the daemon and the agents share one network
(`OCTOPUS_AGENT_NETWORK`, default `octopus-agents`); Postgres is not on it. See
[mcp-server-deploy.md](mcp-server-deploy.md) for the Kubernetes side and
[mcp-integration.md](mcp-integration.md) to add a server of your own.

## Claude Code in the agent images

The published agent images (`octopus-agent`, `octopus-agent-claude-code`)
carry no copy of Claude Code. Their `claude` is a small script that, on first
use, installs the pinned version (`CLAUDE_CODE_VERSION` in
`images/kube-agent/Dockerfile`) unmodified from the npm registry
(`@anthropic-ai/claude-code`), on your machines, and then runs it. Thulu keeps
no copy of the binary anywhere.

- **Docker:** the install lands in a volume per workspace
  (`octopus-engines-<install>-ws-<workspace>`) and one per person for the
  personal-login container, so it happens once per workspace and version, not
  once per run. `thulu uninstall` removes these volumes; the janitor leaves
  them alone.
- **Kubernetes:** pods share no volume, so the chart has the install build its
  own image with Claude Code in it, once per version (see *Kubernetes: the
  agent image the install builds* below). Until that image is ready, or if
  the build fails or is turned off, every agent and chat pod installs Claude
  Code into its own home on first use. That needs egress to
  `registry.npmjs.org` (the tenant template's public-443 rule allows it;
  `egress-strict` does not) and adds the download to each run.
- **Proxy and private registry:** the install is `npm`, so it follows the
  agent's `HTTPS_PROXY`, `NODE_EXTRA_CA_CERTS` and `NPM_CONFIG_REGISTRY`.
- **No route to the npm registry** (air-gapped, `egress-strict`, or to skip the
  per-pod download): build an image with Claude Code installed, and point
  `OCTOPUS_AGENT_CLAUDE_CODE_IMAGE` (the chart's
  `agentRuntime.engineImages.claude-code`) at it. Either from source:

  ```bash
  make kube-agent-image ENGINE=claude-code CLAUDE_CODE_INSTALL=build
  ```

  or on top of the published image, where the script installs into a path
  it checks before anything else:

  ```dockerfile
  FROM ghcr.io/misterdonnuts/octopus-agent-claude-code:<version>
  USER root
  RUN OCTOPUS_ENGINE_DIR=/opt/thulu-engines-image claude --version
  USER 1000:1000
  ```

### Kubernetes: the agent image the install builds

With `agentRuntime.imageBuild.enabled` (the default in kubernetes mode) the
daemon derives an agent image with Claude Code installed from the published
one, inside your cluster, and agent pods use it once it is in the registry.
Nothing leaves the cluster unless you point it at your own registry.

1. On start, the daemon reads the claude-code engine's image
   (`agentRuntime.engineImages.claude-code`, or `agentRuntime.agentImage`) from
   its registry. It builds only when the image's
   `io.thulu.claude-code.install` label says `first-run`. An image built with
   `CLAUDE_CODE_INSTALL=build`, one without the label and one the daemon cannot read are used as they are.
2. It looks in the registry for
   `<repository>:<daemon version>-cc<Claude Code version>-<12 hex of the base digest>`.
   If the image is there, agent pods use it right away.
3. If not, it starts the Job `agent-image-<hash>` in the builder namespace
   (`<release>-builder`): rootless BuildKit, `FROM` the published image by
   digest, and one `RUN` that installs Claude Code where the first-run script
   looks first (`/opt/thulu-engines-image`). The Job pushes the result, and
   the daemon points new agent and chat pods at it. Pods already running keep
   their image.
4. A failed build leaves pods on the published image; the daemon logs why and
   tries again every 30 minutes, and on every restart.

The state shows on Settings → Infrastructure (*Agent image* in the runtime
card) and in the daemon's log (`agent pods run ...`, `agent image build ...
failed`).

**Bundled registry** (`agentRuntime.imageBuild.registry` empty). The chart runs
`registry:2` in the builder namespace on a PVC
(`localRegistry.storage.size`, default `20Gi`). Port 5000 takes pushes behind
htpasswd, with a password the chart generates; port 5001 serves the same
storage read-only and without a password, inside the cluster. Nodes pull from
`localhost:30500` (`localRegistry.hostPort`): a DaemonSet on each node's host
network, listening on `127.0.0.1` only, is a pull-through cache of port 5001. containerd talks plain HTTP to
`localhost` without any node configuration, and nothing off the node can reach
the port. The DaemonSet tolerates every taint so it runs wherever agent pods
can land; the port must be free on every node.

**Agent images you build yourself, in the bundled registry.** When the nodes
cannot pull the published images (a mirror you cannot reach from the cluster,
or images built from your own fork), push the claude-code agent image to port
5000 and name it through the node mirror:

```bash
kubectl -n octopus-builder port-forward svc/registry 5000:5000 &
kubectl -n octopus-builder get secret registry-auth \
  -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d   # user and password
docker login localhost:5000
docker tag <your image> localhost:5000/octopus-agent-claude-code:<tag>
docker push localhost:5000/octopus-agent-claude-code:<tag>
```

```yaml
agentRuntime:
  engineImages:
    claude-code: localhost:30500/octopus-agent-claude-code:<tag>
```

Nodes pull that name from the mirror; the daemon and the BuildKit Job read the
same image through the registry's Service, so the build runs FROM it as with a
published image. Build the image with the default `CLAUDE_CODE_INSTALL=first-run`:
an image that already carries Claude Code needs no build.

**Your own registry.** Set `agentRuntime.imageBuild.registry` (e.g.
`123456789.dkr.ecr.eu-west-1.amazonaws.com/thulu`) and create a push Secret in
the builder namespace, named in `agentRuntime.imageBuild.pushSecret`:

```bash
kubectl -n octopus-builder create secret docker-registry registry-push \
  --docker-server=123456789.dkr.ecr.eu-west-1.amazonaws.com \
  --docker-username=... --docker-password=...
```

The daemon reads it to check for the image and BuildKit pushes with it. Agent
pods get no pull Secret: the nodes must pull from that registry on their own
(ECR through the node's instance role, Artifact Registry through the node's
service account, ACR through the kubelet identity, or a `registries.yaml` /
containerd `hosts.toml` on each node). Pull it once from a node by hand before
you rely on it.

**What it needs from the cluster.**

- The builder namespace is labelled `pod-security.kubernetes.io/enforce:
  privileged`, because rootless BuildKit needs seccomp and AppArmor
  `Unconfined` for its user namespace. The daemon's admission policy
  (`<fullname>-daemon-builds`) holds every Job it writes there to one shape:
  one container running `agentRuntime.imageBuild.builderImage`, non-root, no
  host namespaces, no host paths, no privileged container, no Secret but the
  registry credentials, no ServiceAccount token. Keep everything else out of
  that namespace.
- With `agentRuntime.imageBuild.networkPolicy.enabled` (default) the BuildKit
  pod reaches DNS, the bundled registry and public addresses on 443, minus
  RFC 1918, CGNAT, link-local and every node's address: not tenant pods, the
  daemon or Postgres. An external registry on a private address needs an
  `extraEgress` rule.
- The BuildKit pod asks for 500m CPU and 1Gi (limits 2 CPU, 4Gi) for a few
  minutes per version.
- BuildKit builds for the platform of the node it lands on. On a cluster that
  mixes amd64 and arm64 agent nodes, build the image yourself and turn the
  build off.

**When the daemon does not build.** Agents behind an egress proxy or a
TLS-inspecting CA (`HTTPS_PROXY` forwarded to agents, or `caBundle`): the Job
would need the proxy and the CA too, and a proxy URL can hold a password, which
would then sit in the Job's spec. The daemon says so and keeps the published
image; build the image yourself (above). Turn the build off with
`agentRuntime.imageBuild.enabled: false`: the chart then creates no builder
namespace, registry or policy, and the daemon does nothing.

The bundled registry keeps every image it built: about one per release.
To reclaim the space, delete old tags with the registry API and run
`registry garbage-collect` in its pod, or grow the PVC.

When the install fails, the run's log says why.

## Images for an air-gapped install

Mirror these into your registry, all at the same version tag except the third-
party ones:

| Image | Used by |
|---|---|
| `<registry>/octopus:<version>` | the daemon (dashboard included) |
| `<registry>/octopus-mcp-octopus-api:<version>` | MCP, if ticked |
| `<registry>/octopus-mcp-github:<version>` | MCP, if ticked |
| `<registry>/octopus-mcp-browser:<version>` | MCP, if ticked |
| `<registry>/octopus-mcp-cloud:<version>` | MCP, if ticked |
| `<registry>/octopus-mcp-figma:<version>` | MCP, if ticked |
| `<registry>/octopus-agent-claude-code:<version>` | agents on Claude Code (`OCTOPUS_AGENT_CLAUDE_CODE_IMAGE`, the chart's `agentRuntime.engineImages.claude-code`) |
| `<registry>/octopus-agent-codex:<version>` | agents on Codex (`OCTOPUS_AGENT_CODEX_IMAGE`) |
| `<registry>/octopus-agent-opencode:<version>` | agents on OpenCode (`OCTOPUS_AGENT_OPENCODE_IMAGE`) |
| `<registry>/octopus-agent:<version>` | the three CLIs in one image: a base for your own agent image (`OCTOPUS_AGENT_IMAGE`, the chart's `agentRuntime.agentImage`) |
| `<registry>/octopus-sandbox:<version>` | Kubernetes sandbox pods (the chart's default `agentRuntime.sandboxImage`) |
| `<registry>/octopus-runtime:<version>` | the build-and-test toolset agents run in |
| `postgres:16-alpine` | bundled database; also the wizard's probe of the Docker socket's group |
| `caddy:2-alpine` | `docker-compose.yml` and `docker-compose.caddy.yml` (TLS in front of the daemon) |
| your agent image | `OCTOPUS_AGENT_IMAGE`, or each agent's own image |
| your sandbox image | `OCTOPUS_SANDBOX_IMAGE` (`thulu init` sets `<registry>/octopus-runtime:<version>`; a source build uses `octopus-runtime:latest` from `make agent-images`) |

The public default for `<registry>` is `ghcr.io/misterdonnuts`. Then:

```bash
export OCTOPUS_IMAGE_REGISTRY=registry.corp.example/octopus
thulu init
```

The wizard writes the registry into `deploy/.env` (compose reads it for every
image) or into the values file (`image.repository` and `mcp.imageRegistry`).
The daemon also reads `OCTOPUS_IMAGE_REGISTRY` at runtime and puts it in front
of every agent and sandbox image that names no registry. `thulu apply` pulls
every image from that registry by the digest the signed release manifest names,
so a mirror cannot hand it different content.

`thulu mirror <version> -to <registry> -dir <path>` does the copy: it checks
the release signature, copies every image above (`postgres` and `caddy`
included, by digest) with its signatures, the chart to `<registry>/charts`, and
writes the manifest, deploy bundle and CLI binaries under `-dir` for
`OCTOPUS_UPDATE_SOURCE`. Nothing is re-signed. See
[upgrade-guide.md](upgrade-guide.md#air-gapped-and-mirrored-installs).

## Air-gapped checklist

Everything below assumes no route to the internet. Each line names what breaks
without it.

- [ ] **Images mirrored.** Every image in the table above, including the agent,
  sandbox and runtime images, `postgres` and `caddy`, plus
  `OCTOPUS_IMAGE_REGISTRY` (Docker) or `image.repository` and
  `mcp.imageRegistry` (Helm) pointing at the mirror.
- [ ] **Claude Code inside the agent image.** The published image installs it
  from the npm registry on first use; build one with it installed
  ([Claude Code in the agent images](#claude-code-in-the-agent-images)).
- [ ] **No builds at launch.** Leave `OCTOPUS_BUILD_MISSING_IMAGES` unset. A
  missing image is pulled from the registry the reference names; with no
  registry to pull from, the launch fails and names the image.
- [ ] **TLS without ACME.** Caddy in `docker-compose.yml` and
  `docker-compose.caddy.yml` gets its certificate
  from Let's Encrypt and cannot get one offline. Either give Caddy a certificate from
  your internal CA (a `tls <cert> <key>` line in `deploy/Caddyfile`, with the
  files mounted into the container), or run `docker-compose.standalone.yml` behind your own
  proxy. On Kubernetes, point the ingress at an internal issuer (the chart sets no
  default issuer).
- [ ] **Internal CA trusted everywhere.** Set `OCTOPUS_EXTRA_CA_BUNDLE` to the
  PEM bundle's path inside the daemon container (on Docker, mount the file with
  a `docker-compose.override.yml`) or `caBundle.configMap` on Helm. The daemon
  trusts it, mounts it into every agent and sandbox and sets
  `NODE_EXTRA_CA_CERTS`; the chart also hands it to the MCP servers.
- [ ] **Proxy for agents.** Agents inherit the daemon's `HTTP_PROXY`,
  `HTTPS_PROXY` and `NO_PROXY`. Set `OCTOPUS_AGENT_HTTP_PROXY`,
  `OCTOPUS_AGENT_HTTPS_PROXY` and `OCTOPUS_AGENT_NO_PROXY` when agents need a
  different proxy, or `OCTOPUS_AGENT_PROXY_INHERIT=0` when they need none.
  Thulu adds the daemon, the gateway, loopback and the cluster suffixes to
  `NO_PROXY` itself.
- [ ] **Nonessential traffic off.** Agents start with the CLIs' telemetry,
  auto-update and error reporting switched off. Leave
  `OCTOPUS_CLAUDE_CODE_NONESSENTIAL_TRAFFIC` unset.
- [ ] **LLM endpoint reachable.** Point `OCTOPUS_LLM_UPSTREAM` (Anthropic) or
  `OCTOPUS_LLM_OPENAI_UPSTREAM` at the internal model gateway, or give each
  workspace an LLM profile with its own `base_url`. A plain-`http` base URL
  other than loopback also needs its host in `OCTOPUS_LLM_ALLOW_HTTP_HOSTS`.
- [ ] **Bedrock or Vertex.** Keep the LLM gateway on, so the cloud credential
  stays in the daemon. To run on the daemon's own role instead of a stored key,
  annotate its ServiceAccount (IRSA: `eks.amazonaws.com/role-arn`; GKE:
  `iam.gke.io/gcp-service-account`), set
  `OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY=1`, and with `networkPolicy.enabled` add
  the metadata endpoint to `networkPolicy.extraEgress` (see `values.yaml`). A
  PrivateLink or Private Service Connect endpoint goes in the profile's
  `base_url`, and its host in `OCTOPUS_LLM_ALLOW_HTTP_HOSTS` when it resolves
  to a private address.
- [ ] **GitHub Enterprise Server.** `OCTOPUS_GITHUB_URL` (and
  `OCTOPUS_GITHUB_API_URL` if the API is not at `<url>/api/v3`). Login,
  the GitHub App, agents and the `thulu-github` MCP server all follow it.
- [ ] **Internal webhook and automation targets.** The egress guard refuses
  private addresses. List the internal ranges your webhooks and automation
  `http` steps call in `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS`.
- [ ] **Cloud credential checks.** Stored AWS and GCP credentials are validated
  against the public STS, IAM and Google endpoints. Point
  `OCTOPUS_AWS_STS_ENDPOINT`, `OCTOPUS_AWS_IAM_ENDPOINT`,
  `OCTOPUS_GCP_OAUTH2_ENDPOINT` and `OCTOPUS_GCP_CRM_ENDPOINT` at private
  endpoints, or turn the scheduled check off with
  `OCTOPUS_CREDENTIAL_MONITOR=off`.
- [ ] **Kubernetes: RuntimeClass installed.** The daemon refuses to start
  unless `OCTOPUS_RUNTIME_CLASS` (Helm: `agentRuntime.runtimeClass`, default
  `kata-clh`) names a RuntimeClass the nodes have. Install Kata from your own
  mirror.
- [ ] **Kubernetes: tenant egress.** `OCTOPUS_TENANT_EGRESS_MODE=proxy-only`
  limits agent pods to the control plane and `OCTOPUS_TENANT_EGRESS_ALLOW_CIDRS`
  (your proxy, GHES, artifact registry).
- [ ] **Kubernetes: signature checks.** The Kyverno policy in
  `deploy/k8s-data-plane/kyverno-verify-images.template.yaml` accepts an image
  signed either keyless (checked against Rekor and the Sigstore TUF root) or
  with the release key (checked offline against the public key in the
  policy). Offline, the release-key attestor is the one that passes; nothing
  to configure.
- [ ] **Release source.** `OCTOPUS_UPDATE_SOURCE` set to the `https://` URL
  or `file://` path of the directory `thulu mirror -dir` wrote, or `off`.
  Left at the default, the daemon tries `api.github.com` every 12 hours and
  Settings → Updates shows the failure.

## Environment variables

Grouped by area and checked against the Go code that reads each one. The
wizard writes most of what an install needs into `deploy/.env` or the values
file. `deploy/.env.example` repeats the common ones with examples, and the Helm
chart maps its values onto the same names.

### Install and compose

Read by the compose files and the wizard, not by the daemon.

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_DOMAIN` | none, required by `docker-compose.yml` and `docker-compose.caddy.yml` | public host name Caddy serves and gets a certificate for |
| `OCTOPUS_PG_PASSWORD` | none, required by every compose file | bundled Postgres password; generated by the wizard |
| `OCTOPUS_PG_HOST_PORT` | `5433` | loopback port the bundled Postgres is published on, in every stack but the dev one |
| `OCTOPUS_DOCKER_GID` | none, required by `docker-compose.yml`, `docker-compose.caddy.yml` and the standalone stack | group of the Docker socket on the host; the wizard finds it. `0` only if the socket really is root-group |
| `OCTOPUS_BIND` | `127.0.0.1` | host address the standalone and dev stacks publish on |
| `OCTOPUS_EDGE_SUBNET` | `10.85.85.0/28` | subnet of the network Caddy shares with the daemon in `docker-compose.yml` and `docker-compose.caddy.yml`, and what the daemon trusts `X-Forwarded-For` from |
| `OCTOPUS_VERSION` | none on the standalone stack | tag for the daemon and MCP images |
| `OCTOPUS_IMAGE` | registry + version | full daemon reference; overrides the two above for the daemon |
| `OCTOPUS_MCP_SERVERS` | `octopus-api,thulu-github,thulu-browser` | the checklist's answer, or `none` |
| `COMPOSE_PROFILES` | same as above | which MCP containers compose starts |
| `OCTOPUS_API_KEY` | none, required by `docker-compose.daemon.yml` | that file is the local development pairing; the other stacks do not read it |

### Core and storage

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_DATABASE_URL` | none, required | Postgres DSN for the daemon and the CLI |
| `OCTOPUS_DATA_DIR` | `/data` in the image | data directory for worktrees, logs and attachments; `thulu export` and `import` read it too |
| `OCTOPUS_DATA_VOLUME` | the volume mounted at the data directory | named Docker volume holding `/data`; a daemon in a container mounts subpaths of it into agents. The daemon finds it by inspecting its own container, so set this only if that fails (for example with a custom `hostname:`) |
| `OCTOPUS_PUBLIC_HOST` | `localhost` | host name used in sandbox preview URLs |
| `OCTOPUS_AGENT_RUNTIME` | Docker | `kubernetes` launches agents as pods |
| `OCTOPUS_RUNNERS` | on with Docker, off with `kubernetes` | `1` or `0`: whether machines can register as runners and take the runs of their projects. Off, the runner routes answer 503 and a run of a project on a runner fails |
| `OCTOPUS_AGENT_BASE_URL` | `http://host.docker.internal:8585`; the stacks set `http://octopus:8585` | how agents reach the daemon and the LLM gateway (Docker; Kubernetes uses the Service DNS) |
| `OCTOPUS_GIT_BASE_URL` | unset | daemon git endpoint wired into agent pods (Kubernetes) |
| `OCTOPUS_NAMESPACE` | `POD_NAMESPACE`, else the service account's, else `octopus` | namespace of the daemon and its MCP Deployments |
| `OCTOPUS_LOG_FORMAT` | text | `json` for one JSON line per request |
| `OCTOPUS_AUDIT_SINK` | unset | also write every audit entry to `stdout`, `stderr` or `file:/path` (created `0600`) |
| `OCTOPUS_SAAS_MODE` | off | `1` makes new workspaces start on the Free plan, defaults signup to the allowlist, refuses git remotes on private addresses (see GitHub and git remotes), refuses to start on the Docker agent runtime, refuses settings unsafe with tenants at startup (see [admin-guide.md](admin-guide.md#what-octopus_saas_mode1-refuses-at-startup)), and makes a GitHub App install claim a new installation only with the installer's OAuth code (turn on "Request user authorization (OAuth) during installation" on the App). It defaults `OCTOPUS_BROWSER_MODE` to `per-run`, caps agents at `max(2, CPUs − 2)` when `agent.max_containers` is `0`, limits runs to 60 minutes when `agent.max_run_minutes` is `0`, and lets a workspace admin choose up to 240 minutes when `agent.max_run_minutes_ceiling` is `0`; see [plan limits](admin-guide.md#plan-limits-and-quotas) |
| `OCTOPUS_ALLOW_DOCKER_SAAS` | off | `1` lets SaaS mode start on the Docker agent runtime anyway. Agents of different tenants then share the host kernel and network: for demos only, never real tenants |
| `OCTOPUS_LICENSE_KEY` | unset (Community Edition) | signed Enterprise license |
| `OCTOPUS_SOURCE_URL` | `https://github.com/MisterDonnuts/thulu` | where the dashboard's "Source (AGPL)" link points; set it to your copy's source if you run a modified build (AGPL §13) |
| `OCTOPUS_STRIPE_WEBHOOK_SECRET` | unset (billing webhook answers 503) | verifies Stripe's webhook signatures |

### Cleanup

| Variable | Default | What it does |
|---|---|---|
| `OCTOPUS_CONTAINER_LOG_MAX_SIZE` | `10m` | log rotation size for the containers Thulu launches, when Docker's default log driver is `json-file` or `local`; `-1` turns rotation off |
| `OCTOPUS_CONTAINER_LOG_MAX_FILE` | `3` | rotated log files kept per container |
| `OCTOPUS_ROLLBACK_IMAGE_DAYS` | `7` | how long `thulu update` keeps the previous daemon image for a rollback |
| `OCTOPUS_ROLLBACK_IMAGE`, `OCTOPUS_ROLLBACK_UNTIL` | written by `thulu update` | the image kept for rollback and until when; the updater clears them after a rollback |
| `OCTOPUS_BACKUP_KEEP` | `5` | pre-update snapshots kept in `backups/`, `.sql` and `.tar.gz` together; the kept versions' are always spared |

Retention windows for logs, usage rows and the rest are settings, not
variables; see [admin-guide.md §8](admin-guide.md#8-cleanup-and-retention).

### Sign-in, sessions and SSO

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_JWT_SECRET` | required by the compose files and the chart; a bare daemon makes a random one per start | signs sessions; hex or raw, 16 bytes or more. Changing it logs everyone out |
| `OCTOPUS_ACCESS_TTL` | `30m` | access token lifetime |
| `OCTOPUS_SESSION_MAX_LIFETIME` | `720h` | how long a session can keep refreshing |
| `OCTOPUS_DEV_MODE` | off | `1` turns sign-in off (every request is the owner); loopback only, refused on Kubernetes |
| `OCTOPUS_DEV_MODE_CONTAINER` | off | lets dev mode listen on every interface inside a container |
| `OCTOPUS_OIDC_ISSUER` | unset (OIDC off) | the IdP's issuer URL |
| `OCTOPUS_OIDC_CLIENT_ID`, `OCTOPUS_OIDC_CLIENT_SECRET` | unset | the OIDC client |
| `OCTOPUS_OIDC_NAME` | `SSO` | sign-in button label |
| `OCTOPUS_OIDC_GROUPS_CLAIM` | `groups` | ID-token claim holding the user's groups |
| `OCTOPUS_OIDC_GROUP_ROLE_MAP` | unset | `group:role` pairs; the highest match wins and is reapplied at every sign-in |
| `OCTOPUS_OIDC_REDIRECT_URI` | derived from the request | callback URL, for proxies that rewrite the host |
| `OCTOPUS_FRONTEND_ORIGIN` | derived from the request | dashboard origin, when it is served apart from the daemon |
| `OCTOPUS_SSO_ALLOWED_DOMAINS` | unset (any) | email domains allowed to sign in with SSO |
| `OCTOPUS_SSO_ALLOWED_GROUPS` | unset (any) | IdP groups allowed to sign in |
| `OCTOPUS_SSO_DEFAULT_WORKSPACE` | unset | workspace slug newcomers join on their first sign-in |
| `OCTOPUS_SSO_DEFAULT_ROLE` | `viewer` | role they join it with; `owner` is refused |
| `OCTOPUS_ALLOW_PERSONAL_WORKSPACES` | on, off when OIDC is configured | whether a newcomer with no workspace may create one |
| `OCTOPUS_ALLOW_SSO_AUTOLINK` | off | `1` links an SSO sign-in to an existing account with the same verified email |
| `OCTOPUS_GITHUB_CLIENT_ID`, `OCTOPUS_GITHUB_CLIENT_SECRET` | unset | "Sign in with GitHub" (separate from the GitHub App) |
| `OCTOPUS_OPERATOR_WORKSPACE` | unset | workspace whose admins run install-wide pages: MCP registry, GitHub App, system skill hotfixes, `/metrics`. It has no plan limits unless the operator assigns it a plan |

### API keys and metrics

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_API_KEY_MAX_TTL` | `90d` | longest an API key may live; a Go duration or `Nd`. `0` allows keys without expiry |
| `OCTOPUS_API_KEY` | unset | perimeter key: when set, every request must also present it, on top of a session or a workspace key. It grants nothing by itself |
| `OCTOPUS_METRICS_TOKEN` | unset (an admin of the operator workspace) | bearer token for `GET /metrics` and the full `GET /health` report |

### Network edge

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_ALLOWED_ORIGINS` | unset (same origin only; dev mode adds the local dev-server ports) | CORS allow-list; `*` is an explicit opt-in |
| `OCTOPUS_TRUSTED_PROXIES` | unset (nobody); `docker-compose.yml` sets the edge subnet | IPs or CIDRs allowed to set `X-Forwarded-For`. `private` trusts every private address, agents included |
| `OCTOPUS_RATE_LIMIT_RPS`, `OCTOPUS_RATE_LIMIT_BURST` | `100`, `200` | requests per second and burst, per client IP |
| `OCTOPUS_RATE_LIMIT_PER_KEY`, `OCTOPUS_RATE_LIMIT_PER_KEY_BURST` | `50`, `100` | the same, per API key |
| `OCTOPUS_REQUEST_TIMEOUT` | `45s` | deadline for each request; `0` or `off` removes it |
| `OCTOPUS_TLS_CERT_FILE`, `OCTOPUS_TLS_KEY_FILE` | unset (plain HTTP) | the daemon serves TLS itself; both or neither, reloaded when the files change |

### Stored credentials

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_SECRETBOX_KEY` | unset (derived from `OCTOPUS_JWT_SECRET`, with a warning) | 32-byte key (hex, base64 or raw) that encrypts stored tokens and LLM keys. Generate once and never replace |
| `OCTOPUS_SECRETBOX_KEY_FILE` | unset | the same key from a file, such as a mounted Secret; not together with the variable above |
| `OCTOPUS_SECRETBOX_PREVIOUS_KEYS` | unset | retired keys still accepted while the startup pass moves rows onto the current one |
| `OCTOPUS_SECRETBOX_REWRAP` | on | `0` skips that startup pass |
| `OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT` | off | `1` reads unencrypted rows instead of refusing them |

### Outbound calls

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_EGRESS_GUARD` | on | `off` stops refusing private addresses for webhooks and automation `http` steps; development only |
| `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS` | unset | private ranges webhooks and automations may call; cloud metadata stays blocked |
| `OCTOPUS_PUBLIC_IPS` | unset | comma list of this host's addresses that no interface shows (a container's host, 1:1 NAT); webhooks, automations and SaaS git remotes may not reach them, like the interface addresses |
| `OCTOPUS_EXTRA_CA_BUNDLE` | unset | PEM bundle the daemon trusts and mounts into every agent and sandbox |
| `OCTOPUS_AGENT_HTTP_PROXY`, `OCTOPUS_AGENT_HTTPS_PROXY`, `OCTOPUS_AGENT_NO_PROXY` | the daemon's own `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | proxy settings for agents |
| `OCTOPUS_AGENT_PROXY_INHERIT` | on | `0` stops agents inheriting the daemon's proxy |
| `OCTOPUS_CLAUDE_CODE_NONESSENTIAL_TRAFFIC` | unset (telemetry and auto-update off in agents) | `1` leaves the CLIs' own defaults alone |
| `OCTOPUS_FALLBACK_DNS` | unset | resolvers added after Docker's in agent containers |
| `OCTOPUS_AWS_STS_ENDPOINT`, `OCTOPUS_AWS_IAM_ENDPOINT` | the public AWS endpoints | where stored AWS credentials are validated |
| `OCTOPUS_AWS_SIGNING_REGION` | `us-east-1` | region those calls are signed for |
| `OCTOPUS_GCP_OAUTH2_ENDPOINT`, `OCTOPUS_GCP_CRM_ENDPOINT` | the public Google endpoints | where stored GCP credentials are validated |
| `OCTOPUS_CREDENTIAL_MONITOR` | on | `off` stops the scheduled revalidation of stored credentials |

### GitHub and git remotes

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_GITHUB_URL` | `https://github.com` | web URL of GitHub Enterprise Server |
| `OCTOPUS_GITHUB_API_URL` | `https://api.github.com`, or `<OCTOPUS_GITHUB_URL>/api/v3` | its REST API |
| `OCTOPUS_GITHUB_APP_ID` | unset (App settings come from the dashboard) | GitHub App id; when set, the `OCTOPUS_GITHUB_APP_*` variables override the stored App |
| `OCTOPUS_GITHUB_APP_SLUG` | unset | App slug, for install links |
| `OCTOPUS_GITHUB_APP_CLIENT_ID`, `OCTOPUS_GITHUB_APP_CLIENT_SECRET` | unset | App OAuth credentials |
| `OCTOPUS_GITHUB_APP_PRIVATE_KEY`, `OCTOPUS_GITHUB_APP_PRIVATE_KEY_FILE` | unset | App private key, inline or from a file; inline wins |
| `OCTOPUS_GITHUB_APP_WEBHOOK_SECRET` | unset | verifies GitHub's webhook signatures |
| `OCTOPUS_GITHUB_ALLOW_WORKFLOW_WRITES` | off | `1` lets agents push changes under `.github/workflows/` (the push token then carries `workflows: write`) |
| `OCTOPUS_GIT_ALLOW_SSH` | off | allows `ssh://` and `git@host:` remotes |
| `OCTOPUS_GIT_LOCAL_REMOTE_ROOT` | unset (the dev stacks set `/data/sources`) | allows bare repositories under this directory as remotes |
| `OCTOPUS_GIT_ALLOW_LOCAL_REMOTES` | off | allows any local path, `file://` and loopback remote; single-user development only |
| `OCTOPUS_GIT_ALLOWED_PRIVATE_HOSTS` | unset | SaaS mode only: comma list of remote hosts (names or IPs, exact match) exempt from the git egress check below |

With `OCTOPUS_SAAS_MODE=1`, every git call the daemon makes to a remote
(clone, fetch, push, ls-remote) first resolves the remote's host and refuses
it when any address is private, loopback, link-local, CGNAT, IPv6 ULA,
reserved or one of the server's own (interfaces, `OCTOPUS_PUBLIC_IPS`, the
Kubernetes nodes). git then connects only to the addresses it checked
(`http.curloptResolve`, against DNS rebinding) and does not follow redirects.
A repo_url that fails the check is refused at project create. Self-hosted
installs keep reaching a GitHub Enterprise Server on a private network
without any of this.

### LLM access

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL` | off | `1` lets a workspace with no LLM profile use the operator's key from the agents directory's `.env` |
| `OCTOPUS_LLM_GATEWAY` | `on` in the compose stacks and in the chart's Kubernetes mode; `auto` otherwise (on when `_ADDR` or `_URL` is set) | `on`, `off` or `auto`: agents reach the model through the daemon, which holds the real key or signs for Bedrock and Vertex |
| `OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY` | off | `1` lets a Bedrock or Vertex profile with no credential run on the install's own cloud identity (the daemon's IRSA role or Workload Identity behind the gateway) |
| `OCTOPUS_LLM_GATEWAY_ADDR` | unset | an extra listen address for the gateway |
| `OCTOPUS_LLM_GATEWAY_URL` | from `OCTOPUS_AGENT_BASE_URL` | URL agents are given for the gateway |
| `OCTOPUS_LLM_UPSTREAM` | `https://api.anthropic.com` | Anthropic endpoint behind the gateway |
| `OCTOPUS_LLM_OPENAI_UPSTREAM` | `https://api.openai.com/v1` | OpenAI endpoint behind the gateway |
| `OCTOPUS_LLM_ALLOW_HTTP_HOSTS` | unset (loopback only) | hosts, or `*.suffix`, an LLM profile may reach over plain `http` |

### Agents on Docker

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_AGENT_NETWORK` (or `OCTOPUS_DOCKER_NETWORK`) | `octopus-agents` (dev stack: `octopus-dev-agents`) | network agents, chats and sandboxes join; `bridge` is Docker's default network |
| `OCTOPUS_DOCKER_NETWORK_BRIDGE` | `br-octopus` | Linux bridge name when Thulu creates the network; `auto` lets Docker pick |
| `OCTOPUS_DOCKER_NETWORK_PEERS` | `thulu-browser` | compose services joined to the agent network |
| `OCTOPUS_AGENT_HOST_GATEWAY` | off | `1` maps `host.docker.internal` to the host (native Linux Docker) |
| `OCTOPUS_AGENT_IMAGE` | unset | image for a dashboard-created agent with none of its own; on Kubernetes, the agent pod image |
| `OCTOPUS_IMAGE_REGISTRY` | unset | registry put in front of agent and sandbox images that name none |
| `OCTOPUS_BUILD_MISSING_IMAGES` | off | `1` builds an agent's image in the daemon when it is missing |
| `OCTOPUS_ROOTLESS_AGENTS` | off | `1` runs agents and sandboxes as uid 1000 |
| `OCTOPUS_ALLOW_AGENT_DOCKER_SOCKET` | off | lets an agent mount the Docker socket (root on the host) |
| `OCTOPUS_AGENT_DOCKER_SOCKET_GID` | group of the daemon's socket | group given to an agent that mounts it |
| `OCTOPUS_ALLOW_AGENT_HOST_NETWORK` | off | lets an agent use `network_mode: host` or another container's network |
| `OCTOPUS_SANDBOX_IMAGE` | `<registry>/octopus-runtime:<version>` as `thulu init` writes it on Docker (`octopus-runtime:latest`, built locally, in a source build); required on Kubernetes (the chart sets it) | sandbox image |
| `OCTOPUS_SANDBOX_CPUS` | `2` | CPUs per sandbox; `0` removes the cap |
| `OCTOPUS_SANDBOX_MEMORY_BYTES` | 6 GB | memory per sandbox (`4GB`, `512MB`); `off` removes the cap. Also the pod limit on Kubernetes |
| `OCTOPUS_SANDBOX_PIDS_LIMIT` | `4096` | processes per sandbox |
| `OCTOPUS_SANDBOX_PORTS` | `3000,5173,8000,8080` | ports a dev server in the sandbox can be previewed on (`preview_url`). Docker publishes each one; Kubernetes adds each one to the sandbox Service and opens it only to the task's own pods (and, with `OCTOPUS_BROWSER_MODE=shared`, to the shared thulu-browser) |
| `OCTOPUS_SANDBOX_PUBLISH_IP` | `127.0.0.1` | host address sandbox ports are published on |

### MCP servers

| Variable | Default | Effect |
|---|---|---|
| `MCP_<NAME>_URL` | `http://<name>:3000/sse` for each ticked server | forwarded to every agent |
| `OCTOPUS_MCP_FORWARD_CREDENTIAL` | the ticked servers | servers that receive the run's API key |
| `OCTOPUS_BROWSER_MODE` | `per-run` with `OCTOPUS_SAAS_MODE=1`, else `shared` | `per-run` starts one thulu-browser per run granted it, next to the agent (sidecar in the agent pod, or a container in the agent's network namespace); `shared` uses the one at `MCP_THULU_BROWSER_URL` (with `OCTOPUS_SAAS_MODE=1` the daemon logs a warning at boot: every workspace's scripts would run in one process). See [`mcp-integration.md`](mcp-integration.md#per-run-thulu-browser) |
| `OCTOPUS_BROWSER_IMAGE` | `OCTOPUS_MCP_BROWSER_IMAGE`, else `<registry>/octopus-mcp-browser:<version>` | image of the per-run browser |
| `OCTOPUS_BROWSER_PORT` | `3010` | loopback port the per-run browser listens on |
| `OCTOPUS_BROWSER_CPU_REQUEST`, `OCTOPUS_BROWSER_CPU_LIMIT` | `500m`, `1` | CPU of the per-run browser (Docker applies the limit only) |
| `OCTOPUS_BROWSER_MEMORY_REQUEST`, `OCTOPUS_BROWSER_MEMORY_LIMIT` | `1Gi`, `1536Mi` | memory of the per-run browser; the limit must fit `OCTOPUS_TENANT_LIMIT_MAX_MEMORY` |

### Kubernetes runtime

The chart sets most of these from its values.

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_KUBE_INCLUSTER` | `false` (the chart sets `true`) | use the pod's service account |
| `OCTOPUS_KUBECONFIG`, `OCTOPUS_KUBE_CONTEXT` | the client's defaults | kubeconfig and context when the daemon runs outside the cluster |
| `OCTOPUS_RUNTIME_CLASS` | required | RuntimeClass of every agent and sandbox pod (the chart's default is `kata-clh`) |
| `OCTOPUS_KUBE_ALLOW_NO_RUNTIMECLASS` | off | `1` starts without one; development clusters only |
| `OCTOPUS_CONTROL_PLANE_NAMESPACE` | `POD_NAMESPACE` | namespace tenant pods may reach the daemon and MCP servers in |
| `OCTOPUS_TENANT_CONTROL_PLANE_PORTS` | `8585,8586,3000` | ports they may reach there |
| `OCTOPUS_CONTROL_PLANE_POD_SELECTOR` | `app.kubernetes.io/name=octopus` (the chart sets the daemon's full selector) | `key=value` labels of the pods in that namespace allowed to open connections into tenant pods: the daemon's. MCP servers and anything else there are refused |
| `OCTOPUS_TENANT_INGRESS_PORTS` | `8587` | tenant pod ports those pods may reach (the exec agent of sandbox and chat pods) |
| `OCTOPUS_TENANT_DNS_NAMESPACE` | `kube-system` | namespace of the cluster DNS |
| `OCTOPUS_TENANT_POD_SECURITY` | `restricted` | Pod Security level of tenant namespaces (`restricted` or `baseline`) |
| `OCTOPUS_TENANT_EGRESS_MODE` | `open` | `open`: HTTPS to public addresses plus the allow-list. `proxy-only`: the control plane and the allow-list |
| `OCTOPUS_TENANT_EGRESS_ALLOW_CIDRS` | unset | extra `CIDR[:port]` destinations (proxy, GHES, registry) |
| `OCTOPUS_TENANT_PRIVATE_CIDRS` | RFC 1918 and `100.64.0.0/10` | ranges `open` mode keeps agents out of. Link-local and every node's InternalIP and ExternalIP (read from the API at each launch; needs `get`/`list` on nodes, which the chart grants) are kept out on top |
| `OCTOPUS_TENANT_QUOTA` | on | `off` skips the ResourceQuota and LimitRange per tenant namespace |
| `OCTOPUS_TENANT_QUOTA_PODS` | `20` | non-terminal pods per tenant namespace |
| `OCTOPUS_TENANT_QUOTA_CPU_REQUESTS`, `OCTOPUS_TENANT_QUOTA_MEMORY_REQUESTS` | `20`, `64Gi` | `requests.cpu` and `requests.memory`: what a workspace's pods may reserve together. This is what the quota admits pods on |
| `OCTOPUS_TENANT_QUOTA_CPU` | unset | optional `limits.cpu` total, a ceiling on how far the workspace's pods may burst together. Unset or `off` leaves it out: CPU past a pod's share is throttled, never killed, and under Kata each microVM is sized from its own pod's limit |
| `OCTOPUS_TENANT_QUOTA_MEMORY` | `128Gi` | `limits.memory` total, the overcommit ceiling on the workspace's memory limits. `off` leaves it out |
| `OCTOPUS_TENANT_QUOTA_STORAGE` | `500Gi` | the budget of the task worktree claims (10Gi each); the namespace's `requests.storage` is that plus the cache claim, so `500Gi` becomes `520Gi` |
| `OCTOPUS_TENANT_LIMIT_MAX_CPU`, `OCTOPUS_TENANT_LIMIT_MAX_MEMORY` | `4`, `8Gi` | the ceiling per container |
| `OCTOPUS_TENANT_SECRETS_CLUSTERROLE` | unset | ClusterRole the daemon binds to itself in each tenant namespace to write Secrets and the CA ConfigMap |
| `OCTOPUS_SANDBOX_NAMESPACE` | `octopus-sandboxes` | namespace for sandboxes with no workspace |
| `OCTOPUS_AGENT_IMAGE_BUILD` | off (the chart sets `true` unless `agentRuntime.imageBuild.enabled` is false) | build the agent image with Claude Code installed (*Kubernetes: the agent image the install builds*) |
| `OCTOPUS_AGENT_IMAGE_BUILD_NAMESPACE` | unset | namespace of the BuildKit Job; the build stays off without it |
| `OCTOPUS_AGENT_IMAGE_BUILD_PUSH_REGISTRY` | unset | registry path the Job pushes to and the daemon checks |
| `OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN` | unset | `1` lets the installation owner store and use a Claude subscription token (`sk-ant-oat…`) while agents run in Kubernetes; unset, nobody can |
| `OCTOPUS_AGENT_IMAGE_BUILD_PULL_REGISTRY` | the push registry | registry path agent pods name (`localhost:30500` for the bundled registry) |
| `OCTOPUS_AGENT_IMAGE_BUILD_INSECURE` | off | plain HTTP to the push registry (the bundled one) |
| `OCTOPUS_AGENT_IMAGE_BUILD_AUTH_SECRET` | unset | `kubernetes.io/dockerconfigjson` Secret in that namespace with push access |
| `OCTOPUS_AGENT_IMAGE_BUILD_BUILDER_IMAGE` | unset | the BuildKit image, by digest; must match the admission policy |
| `OCTOPUS_SANDBOX_CPU_REQUEST`, `OCTOPUS_SANDBOX_CPU_LIMIT` | `250m`, `2` | sandbox pod CPU |
| `OCTOPUS_SANDBOX_MEMORY_REQUEST` | `512Mi` | sandbox pod memory request |
| `OCTOPUS_SANDBOX_EPHEMERAL_REQUEST`, `OCTOPUS_SANDBOX_EPHEMERAL_LIMIT` | `1Gi`, `20Gi` | sandbox pod ephemeral storage |
| `OCTOPUS_SANDBOX_CACHE_SIZE` | `20Gi` | size of the `octopus-cache` claim each tenant namespace gets: the dependency cache every sandbox of the workspace mounts at `/cache` (npm, Go, pip, mise toolchains). `off` mounts no cache. The claim is ReadWriteOnce, which only works on a single node; a multi-node cluster needs a ReadWriteMany storage class |
| `OCTOPUS_KUBE_AGENT_CPU_REQUEST`, `OCTOPUS_KUBE_AGENT_CPU_LIMIT` | `250m`, `2` | agent pod CPU |
| `OCTOPUS_KUBE_AGENT_MEMORY_REQUEST`, `OCTOPUS_KUBE_AGENT_MEMORY_LIMIT` | `512Mi`, `6Gi` | agent pod memory |
| `OCTOPUS_KUBE_AGENT_EPHEMERAL_REQUEST`, `OCTOPUS_KUBE_AGENT_EPHEMERAL_LIMIT` | `1Gi`, `20Gi` | agent pod ephemeral storage |
| `OCTOPUS_KUBE_EMPTYDIR_SIZE_LIMIT` | `10Gi` | size limit of the worktree `emptyDir` in agent pods |

### CLI

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_URL` | `http://localhost:8585` | daemon the `thulu` commands talk to |
| `OCTOPUS_HOME` | `~/.octopus` | where an install without a checkout lives |
| `OCTOPUS_RELEASE_REPO` | `MisterDonnuts/thulu` | repository `thulu init` downloads the first release from |
| `OCTOPUS_CHART_REGISTRY` | `<OCTOPUS_IMAGE_REGISTRY>/charts` | where `thulu apply` takes the chart from on Kubernetes |
| `OCTOPUS_KEEP_VERSIONS` | `3` | versions a Docker install keeps (images and snapshot) for going back without a registry; minimum 2 |

### Updates

Read by the daemon (what Settings → Updates shows) and by `thulu update`.
Whatever the source, every manifest is checked against the release key built
into the binary before anything in it is used.

| Variable | Default | Effect |
|---|---|---|
| `OCTOPUS_UPDATE_SOURCE` | `github` | where releases are looked up: `github`, the `https://` URL of a mirror written by `thulu mirror -dir`, `file:///abs/path` to the same, or `off` |
| `OCTOPUS_UPDATE_CHANNELS` | `stable` | channels offered: `stable`, `prerelease`, `preview` (branch builds, signed by the preview workflow; the CLI needs `cosign`) |
| `OCTOPUS_UPDATE_GITHUB_TOKEN` | unset | read-only token for a private fork; never logged |
| `OCTOPUS_UPDATE_INTERVAL` | `12h` | how often the daemon checks; minimum `1h` |

Helm sets the first two from `updates.source` and `updates.channels`, and sets
`OCTOPUS_IMAGE` so the daemon knows its own image.

### Set by Thulu

The daemon or the chart sets these; setting them by hand changes nothing or
breaks a run: `OCTOPUS_API` and the per-run `OCTOPUS_API_KEY` inside agents,
`OCTOPUS_EXEC_TOKEN`, `OCTOPUS_EXEC_ADDR`, `OCTOPUS_RESULT_REF`,
`OCTOPUS_SERVICE_ACCOUNT`, `OCTOPUS_LLM_SOURCE`, `OCTOPUS_LLM_PROFILE`,
`OCTOPUS_VERTEX_CREDENTIALS_B64`, `OCTOPUS_DISABLE_NONESSENTIAL_TRAFFIC`. Tests
read `OCTOPUS_TEST_POSTGRES_DSN`.

## Rotating the operator key

The key `thulu init` mints expires after 90 days, and so does any key minted
without an expiry. Before it lapses, mint a replacement in the daemon
container, update whatever uses the old key, then revoke the old one from the
dashboard or with `DELETE /auth/api-keys/{id}`:

```bash
docker compose exec octopus ./octopus workspace list
docker compose exec octopus ./octopus keys mint -workspace <id> \
  -role owner -permissions full -name "operator key 2026-Q4" -expires-in-days 90
```

`OCTOPUS_API_KEY_MAX_TTL=0` goes back to keys without expiry.

Updates between releases are in [upgrade-guide.md](upgrade-guide.md).
