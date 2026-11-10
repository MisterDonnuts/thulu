# Security Hardening Checklist

A post-install hardening guide for security and platform teams running Thulu
in production. Work through each section; the final table maps Thulu controls
to SOC 2 / ISO 27001 and notes the current gaps honestly.

Related: [admin-guide.md](admin-guide.md) ·
[upgrade-guide.md](upgrade-guide.md) ·
[troubleshooting.md](troubleshooting.md) ·
[auth.md](auth.md) · [architecture.md](architecture.md)

---

## 1. Secrets: JWT signing key

`OCTOPUS_JWT_SECRET` signs sessions and OIDC state; it is also the fallback
key for stored secrets when `OCTOPUS_SECRETBOX_KEY` is unset. Treat it as a
root credential.

```bash
openssl rand -hex 32      # 32 bytes / 64 hex chars
```

- **Required outside dev mode.** The daemon **fails closed**: with no
  `OCTOPUS_JWT_SECRET` (and `OCTOPUS_DEV_MODE` not set) startup is refused.
  Minimum accepted key material is 16 bytes (32 hex chars); use 32 bytes.
- **Keep it stable.** If the secret changes (or is auto-generated because it was
  unset), **all existing tokens are invalidated** — every session breaks and you
  can hit a `/login` ↔ `/` redirect loop (see
  [troubleshooting.md](troubleshooting.md)). Persist it in your secret store, not
  inline in compose.
- **Give stored secrets their own key.** Without `OCTOPUS_SECRETBOX_KEY` the key
  that encrypts LLM keys, GitHub App secrets, credentials and webhook secrets is
  derived from `OCTOPUS_JWT_SECRET`, and rotating the JWT secret makes all of
  them unreadable. See [section 3](#stored-secret-encryption-key-secretbox).

### Never run `OCTOPUS_DEV_MODE` in production

`OCTOPUS_DEV_MODE=1` turns off logins. The daemon holds a random token
(`OCTOPUS_DEV_TOKEN`, or `dev-token` in its data dir) and prints a link that
carries it; `thulu open` opens the same link. The first visit trades the token
for an HttpOnly, SameSite=Strict cookie and redirects to the same page without
it. The browser holding that cookie is the owner; writes from it need the CSRF
header like a login session's. A request with neither the cookie nor an API key
gets 401, so a script, a container or another page on loopback is not the
owner by being there. As a safety interlock the daemon **refuses to start in dev mode unless it
binds to loopback** (`127.0.0.1`), and **refuses it outright inside Kubernetes**
(`KUBERNETES_SERVICE_HOST` set): a pod's loopback is still reachable through a
port-forward or a sidecar, so the loopback check does not hold there. Do not set
it on any networked deployment.

---

## 2. Network exposure

- **Terminate TLS at the edge.** The bundled Caddy front-end does automatic
  Let's Encrypt TLS 1.2/1.3 and adds HSTS, `X-Content-Type-Options`,
  `X-Frame-Options: DENY`, and `Referrer-Policy`. Put Caddy (or your own
  reverse proxy/TLS terminator) in front; don't expose the daemon's plain HTTP
  port to clients directly.
- **Or let the daemon serve TLS.** With `OCTOPUS_TLS_CERT_FILE` and
  `OCTOPUS_TLS_KEY_FILE` set, the daemon serves HTTPS only, TLS 1.2 minimum,
  and sends HSTS (`max-age=31536000`) on those connections. Use it where there is
  no proxy, or to encrypt the proxy→daemon hop. Setting only one of the two
  fails the start. A renewed certificate on disk (cert-manager, certbot) is
  picked up within 30 seconds without a restart; one that does not load is
  logged and the previous one keeps serving. Health probes and the CLI then
  need `https://`. A reverse proxy is still the usual edge.
- **Port posture:**
  - Expose **80/443** (Caddy) to clients. 80 is needed for the ACME challenge
    and redirects to 443.
  - Keep the daemon's **8585** internal. In the bundled compose it is only
    `expose`d on the bridge network (reachable by Caddy), never published to the
    host.
- **Trust proxies correctly.** Set `OCTOPUS_TRUSTED_PROXIES` so the daemon only
  honours `X-Forwarded-For` from your proxy: an explicit list of proxy
  IPs/CIDRs. The bundled `docker-compose.yml` does this for you: Caddy and the
  daemon share an `edge` network with a fixed subnet (`OCTOPUS_EDGE_SUBNET`,
  default `10.85.85.0/28`) and that subnet is the default. The standalone stack
  and the Helm chart trust nobody until you name your proxy or the ingress
  controller's pod CIDR (`config.trustedProxies`). Without it, rate limiting
  and audit entries record the proxy's IP instead of the real client. Avoid
  `private` (loopback + RFC1918/ULA) anywhere agents run: agent containers and
  pods have private addresses too and could forge the client address. Inside
  Kubernetes the daemon logs a warning when it sees `private`. A hop that is
  not an IP address makes the daemon use the connecting peer instead.
- **Cross-origin callers.** `OCTOPUS_ALLOWED_ORIGINS` unset means no CORS
  origin is allowed: the dashboard is served same-origin and needs none. The
  local dev-server ports (`localhost:4321`, `:3000`, `:5173`) are the default
  only in dev mode. A split deployment (dashboard on another origin) must list it.
- **Outbound requests (SSRF).** Automation `http` steps and webhook deliveries
  refuse loopback, private (RFC 1918, CGNAT, ULA), link-local and reserved addresses,
  checked on the address actually dialed, so DNS rebinding and redirects do not
  get around it. Cloud metadata endpoints (`169.254.169.254`, ECS
  `169.254.170.2`, Alibaba `100.100.100.200`, `fd00:ec2::254`) are refused
  always. So are the host's own addresses, public ones included, because a
  request to the host's public IP reaches the ingress, NodePorts and published
  ports: the daemon reads them from its network interfaces (again every
  minute), from the nodes in Kubernetes mode, and from `OCTOPUS_PUBLIC_IPS`.
  Set that variable to the server's public address when the daemon runs in a
  container or behind 1:1 NAT, where no interface carries it. The MCP health probe may reach private and loopback addresses (MCP
  servers live next to the daemon) but not link-local or metadata. To let a webhook or step
  reach an internal service, add its range to
  `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS` (comma list of CIDRs).
  `OCTOPUS_EGRESS_GUARD=off` disables the guard entirely.
- **Git remotes on a shared install.** With `OCTOPUS_SAAS_MODE=1` the daemon
  applies the same rules to every remote it clones, fetches or pushes: the
  host's addresses are resolved and checked before git runs, git connects
  only to those addresses, and redirects are not followed. A repo_url on a
  private network needs its host in `OCTOPUS_GIT_ALLOWED_PRIVATE_HOSTS`.
- **Protect `/metrics`.** Set `OCTOPUS_METRICS_TOKEN` (e.g.
  `openssl rand -hex 32`); Prometheus must then send
  `Authorization: Bearer <token>`. If unset, `/metrics` needs an admin session
  in the operator workspace (`OCTOPUS_OPERATOR_WORKSPACE`), because the series
  cover every workspace's traffic and spend. Either way it is never anonymous.
- **`/health` details follow the same rule.** The probes stay unauthenticated,
  but anonymous callers and members of other workspaces get only
  `{"status": …}` (and `/health/ready` drops the failure reason). The full
  report, with every check's error, uptime, Go version and free disk, goes to
  the operator (an admin session in `OCTOPUS_OPERATOR_WORKSPACE`, or dev mode)
  and to a caller sending `Authorization: Bearer <OCTOPUS_METRICS_TOKEN>`.
- **What tenants see of the install.** Outside the operator workspace, `GET
  /mcps` lists names and states without URLs or images and skips the health
  pings, `GET /onboarding` leaves out the identity-provider and workspace
  steps, and `GET /settings` returns only `ingest.public_url` (other keys
  answer 403). Rows whose `workspace_id` is empty are hidden from every
  workspace, the operator's included (only a dev-mode caller reads them). On a
  single-workspace install the startup backfill stamps them; with more than one
  workspace, assign them by hand
  (`UPDATE <table> SET workspace_id = '<id>' WHERE workspace_id = ''`).

```yaml
# prometheus.yml
scrape_configs:
  - job_name: octopus
    authorization: { type: Bearer, credentials: "<OCTOPUS_METRICS_TOKEN>" }
    static_configs: [{ targets: ["thulu.example.com"] }]
```

---

## 3. Secrets management

| Secret | Where it lives | Rotation |
|--------|----------------|----------|
| `OCTOPUS_JWT_SECRET` | Environment (your secret store → compose env). | Rotating invalidates all sessions — schedule during a maintenance window. Without `OCTOPUS_SECRETBOX_KEY` it also locks every stored secret; set that first. |
| `OCTOPUS_SECRETBOX_KEY` | Environment or a mounted file (`OCTOPUS_SECRETBOX_KEY_FILE`). | See below; rotation keeps the old key readable. |
| OIDC client secret (`OCTOPUS_OIDC_CLIENT_SECRET`) | Environment. | Rotate at the IdP, then update env + restart. |
| GitHub OAuth secret (`OCTOPUS_GITHUB_CLIENT_SECRET`) | Environment. | Same as OIDC. |
| Per-project GitHub tokens | Encrypted at rest in Postgres. | Re-enter the token to replace it. |
| API keys | SHA-256 **hash** only, in Postgres; plaintext shown once. | Create new → swap consumer → revoke old (see admin guide). Every key expires, 90 days at most by default (`OCTOPUS_API_KEY_MAX_TTL`). |
| Webhook signing secrets | Encrypted (secretbox); shown once. | `POST /webhooks/{id}/rotate-secret`. |

- Inject env secrets from a real secret store (Docker/Swarm secrets, Vault,
  SOPS) rather than committing a populated `.env`.
- **Data directory permissions:** the daemon creates `/data` and its
  subdirectories with mode `0750`. The volume holds project worktrees, task
  attachments, and recent logs — sensitive but not credential material.
  Restrict host access to the volume to the Thulu runtime user/group;
  `0700` on the host mount point is a reasonable tightening if no other
  user needs it.
- **Postgres credentials:** the daemon authenticates with
  `OCTOPUS_DATABASE_URL`. Use a dedicated database role with only the
  privileges Thulu needs (CONNECT, CREATE schema objects in its database,
  no SUPERUSER); rotate via env-var swap and restart.
- The database holds **no user credentials** — identity lives at the provider.
  It does contain HMAC API-key hashes, **encrypted** GitHub tokens and LLM keys,
  and audit history —
  treat the Postgres data directory and any `pg_dump` artifacts as the
  primary thing to encrypt at rest and tightly scope access to.

### Stored-secret encryption key (secretbox)

LLM keys, LLM profile keys, the GitHub App private key and secrets, credential
payloads and webhook signing secrets are encrypted with AES-256-GCM. The key:

```bash
openssl rand -hex 32   # at least 32 bytes; hex, base64 or raw text are accepted
```

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_SECRETBOX_KEY` | unset | The current key. Unset derives it from `OCTOPUS_JWT_SECRET` and logs a warning at start. |
| `OCTOPUS_SECRETBOX_KEY_FILE` | unset | Path to a file holding the key (a mounted Kubernetes or Docker secret). Setting both this and `OCTOPUS_SECRETBOX_KEY` fails the start. |
| `OCTOPUS_SECRETBOX_PREVIOUS_KEYS` | unset | Comma list of retired keys, still accepted for reading. |
| `OCTOPUS_SECRETBOX_REWRAP` | on | At every start, rewrite stored values onto the current key. `0` skips the rewrite. |
| `OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT` | off | Accept a stored value that was never encrypted. See the plaintext note below. |

Values written with a dedicated key look like `v2:<key id>:<ciphertext>`; the
key id is the first 8 hex characters of the key's SHA-256, so the daemon knows
which key opens a value and an unknown one fails with a clear error.

**Adopting a dedicated key (upgrade).** Set `OCTOPUS_SECRETBOX_KEY` and restart,
keeping `OCTOPUS_JWT_SECRET` unchanged. On start the daemon rewrites every stored
secret: v1 values (JWT-derived) become v2, and any unencrypted row
is encrypted. Rows that do not decrypt are logged, counted and left
untouched. Once the log shows no failures, `OCTOPUS_JWT_SECRET` can be rotated
without touching stored secrets.

**Rotating the key.** Move the current key into
`OCTOPUS_SECRETBOX_PREVIOUS_KEYS`, put the new one in `OCTOPUS_SECRETBOX_KEY`, and
restart. The start rewrites every value onto the new key; after a start that
reports no failures, drop the old key from the list.

**Plaintext rows.** A stored value without an encryption prefix is refused, so
a row written straight into the database is never accepted as a secret. The startup rewrite encrypts such
rows, so normally nothing needs doing; set
`OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT=1` only if the rewrite is off and old
plaintext rows must keep working.

Limit: the ciphertext is not bound to its row (no associated data), so a
database writer can swap one encrypted value for another of the same kind.

---

## 4. Multi-tenancy & workspace isolation

- Projects, tasks, and audit entries are **workspace-scoped**: reads and the
  audit query are filtered to the caller's workspace.
- Membership and role are stored per workspace in `workspace_members`; a user's
  effective permissions are re-derived from that table on login (no stale-token
  privilege after a role change once the token is refreshed).
- **Single-workspace posture:** simplest to reason about — one team, one trust
  boundary. New SSO users can be funnelled into it with
  `OCTOPUS_SSO_DEFAULT_WORKSPACE`, joining as `OCTOPUS_SSO_DEFAULT_ROLE`
  (`viewer` unless set).
- **Multi-workspace posture:** isolation is logical (row-level scoping in
  shared Postgres tables), not physical. All workspaces share one daemon, one
  process, and one schema. For hard tenant isolation (separate blast radius),
  run separate Thulu instances against separate Postgres databases rather
  than relying on in-process scoping.
- **Docker runtime: no isolation between runs.** Agent, sandbox and chat
  containers of every workspace run on one Docker host and mount their task
  worktrees from one data volume. For git to work inside them, each container
  also gets the project clone's `.git` directory, read-only, at the path the
  worktree's `.git` file names. A run can therefore read every branch and
  object of that project, including other tasks' branches. It cannot write
  there: the daemon runs git on that clone with the Docker socket in reach and
  relies on its `.git/config` and worktree metadata being out of an agent's
  reach, so commits go through the daemon (`thulu-github`'s `commit` tool).
  The Kubernetes runtime clones each task into its own volume, in the tenant's
  namespace, and is the one to use when workspaces must not see each other.

---

## 5. Identity hardening

- Enforce SSO and **require verified email** (already mandatory — unverified
  emails are rejected for both OIDC and GitHub).
- **Restrict who may sign in.** A Google or GitHub provider vouches for any
  account on the internet. Set `OCTOPUS_SSO_ALLOWED_DOMAINS` (exact email
  domains) and/or `OCTOPUS_SSO_ALLOWED_GROUPS` (IdP groups from the groups
  claim). Both are checked on every sign-in, so removing someone from the group
  in the IdP locks them out at their next login. GitHub sign-in carries no
  groups and is refused while a group list is set.
- **No self-founded tenants.** With OIDC configured,
  `OCTOPUS_ALLOW_PERSONAL_WORKSPACES` defaults to off: a newcomer who is not
  placed by `OCTOPUS_SSO_DEFAULT_WORKSPACE` gets an account with no workspace
  and waits for an admin to add them (`user.pending_access` in the audit log).
  The first sign-in on an empty install still gets a workspace, so a fresh
  install has an owner. `1` restores self-service workspaces.
- Keep `OCTOPUS_ALLOW_SSO_AUTOLINK=0` unless every trusted IdP's email
  verification is authoritative (auto-link is an account-takeover vector).
- **Require MFA at the identity provider** for `admin`/`owner` accounts.
  Thulu stores no credential and enforces no second factor of its own, so the
  provider's policy is the policy.
- Lockout and password strength are the provider's job for the same reason.
- Cutting someone off means deactivating them in the provider; to evict a live
  session immediately, also remove them from the workspace (revokes refresh
  tokens and bumps `token_version`).

---

## 6. Agent credential model

Agents do **not** hold the operator master key. For each agent run the dispatcher
mints a **task-scoped API key** that is:

- **Capped at `developer`** — a task-scoped key cannot reach `admin`/`owner`
  surfaces (it is explicitly rejected from member management, workspace
  administration, key minting, etc.).
- **Scoped to its own task** — internal endpoints check the key's `scope_task_id`
  and reject access to other tasks/projects.
- **Auto-expiring** — the key carries an expiry (TTL) and stops working after it.
- **Revoked on run exit** — the dispatcher revokes every key scoped to a task
  when that task's agent run finishes.

A leaked agent credential therefore yields, at most, time-boxed developer-level
access to a single task — not the whole instance.

### What a run's key may touch inside its workspace

The workspace boundary and the developer cap do not say which task a run may
act on. Without a finer rule, a prompt-injected agent on task A could relaunch
task B, rewrite it, decide its approvals or read its run's transcript. The daemon
applies this rule to every run key (task runs, mention runs, chats and
automations), at the route layer (`internal/api/run_scope.go`), so a new route
under `/tasks/{id}` inherits it:

| Surface | A run's key may |
|---|---|
| Board data: task list and detail, comments, sections, approvals and flags lists, dependencies, attachments, events, brief, projects, boards, stages, usage totals | read anything in its workspace |
| Create a task (`POST /tasks`) | yes; the new task records the run's task in `origin_task_id` |
| Change a task: `PATCH`/`DELETE /tasks/{id}` and every write under it (subtasks, clone, fork, relaunch, ensure-pr, comments, converse, attachments, asking for an approval (`POST /approvals`, `POST /flags`), auditor feedback, sections, dependencies, sandbox), `DELETE /task-attachments/{id}` | only on its own task, its parent, its sibling and child subtasks, and tasks whose `origin_task_id` is its task |
| Decide or delete an approval (`POST /tasks/{id}/approvals/{aid}/decide`, `DELETE /tasks/{id}/approvals/{aid}`, and the deprecated `PATCH`/`DELETE /tasks/{id}/flags/{flagId}`) | never, not even on its own task: approvals are the human gate |
| Run transcripts: `/usage/sessions/{id}/*`, `/agents/{id}/logs`, `/agents/{id}/files`, the `logs:<run>` topic on `/events` | only for runs launched on a task it may change |
| Chats: `/chats/{id}/*` | only its own chat; `POST /chats` is refused, `GET /chats` and `/chats/events` show only its own chat, and the `chat` topic on `/events` is refused |
| `/internal/tasks/{id}/*`, the sandbox, git push | unchanged: its own task (and, for the sandbox, the tasks sharing its container) |

Refusals answer `403` with a message that says what the key may touch. A task
the caller cannot see at all still answers `404`, so the rule never confirms
that another tenant's task exists.

Ownership of created tasks is per task, not per run: an agent that runs on
task A in a later stage can still edit the tasks an earlier stage's agent split off. The
origin comes from the key, never from the request body.

A chat that is not linked to a task gets no `tasks:write` at all and answers
for no task. A chat linked to a task acts with that task's rights. A chat acts
for the person talking to it, but its key does not carry that person's
identity, so it cannot borrow their session's rights: to change another task,
the person does it from the dashboard or asks the agent on that task.

What agents see:

- The `octopus-api` MCP tools that take a task id (`task_update`,
  `task_add_comment`, `task_relaunch`, `task_move_to_board`, `task_section_set`,
  `task_attach`, `task_clone`, `task_add_dependency`, …) fail with `403` on
  a task outside the run's group and its created tasks. Planning flows
  create tasks or subtasks, then edit them.
- An agent can ask for an approval but cannot approve, waive or delete one.
- An agent cannot read other tasks' run transcripts or other chats.

---

## 7. Logging & observability

- Set `OCTOPUS_LOG_FORMAT=json` (default in the production compose) for
  machine-parseable logs; ship them to your SIEM.
- Audit log (the `audit_log` table in Postgres) covers auth and membership events; export via
  `GET /audit` (audit export is an EE capability). Retain per your policy.
  Each entry names the actor (user, API key by id and name, or agent run), the
  workspace of the credential, and the client IP after trusted-proxy handling.
- **Ship the audit log off the box.** `OCTOPUS_AUDIT_SINK=stdout` (or `stderr`,
  or `file:/path/audit.log`, created `0600`) writes every entry as one JSON line
  as it is recorded. The rows are hash-chained; `GET /audit/verify` (admin of
  `OCTOPUS_OPERATOR_WORKSPACE`) recomputes the chain and returns the head hash.
  Record that hash outside the database: the chain finds an edited, inserted or
  deleted row, but a truncated tail only shows against a head recorded earlier
  or the sink's copy. A database superuser can recompute the whole chain; the
  sink is the copy they cannot edit.
- **Per-key rate limit.** Beyond the per-IP limit, each API key gets its own
  bucket: `OCTOPUS_RATE_LIMIT_PER_KEY` (default 50 req/s) and
  `OCTOPUS_RATE_LIMIT_PER_KEY_BURST` (default 100). `0`/`0` turns it off. A
  runaway integration then exhausts its own bucket, not the office's shared IP.
- Health probes: `/health/live` (liveness), `/health/ready` (DB readiness),
  `/health` (component detail) — all unauthenticated by design for probes.

---

## 8. Control mapping (SOC 2 / ISO 27001)

Honest mapping of control areas to current Thulu features, with gaps called
out. This is an aid for your own assessment, not a certification.

| Control area | Thulu feature | Status / gap |
|--------------|-----------------|--------------|
| **Logical access control** (CC6.1) | Role model (viewer/developer/admin/owner); workspace-scoped membership; fail-closed auth; accounts provisioned only by a federated sign-in. | Covered. |
| **Authentication strength** (CC6.1) | Delegated to the identity provider over OIDC, with mandatory verified email and anti-takeover autolink default-off. Thulu stores no credential. | Covered — password policy, MFA and lockout are evidenced from the IdP. |
| **Least privilege for automation** (CC6.1/CC6.3) | Task-scoped, developer-capped, auto-expiring, auto-revoked agent keys. | Covered. |
| **Encryption in transit** (CC6.7) | Caddy TLS 1.2/1.3 + HSTS at the edge, or the daemon's own TLS (`OCTOPUS_TLS_CERT_FILE`, TLS 1.2+). | Covered when fronted by Caddy/TLS proxy (default deploy) or with daemon TLS. Daemon→DB is local. |
| **Encryption at rest** (CC6.7) | GitHub tokens, LLM keys, credentials and webhook secrets encrypted in Postgres with a dedicated, rotatable key (`OCTOPUS_SECRETBOX_KEY`); API-key hashes only. | **Gap:** no full at-rest DB encryption — non-secret tables are stored in cleartext. Mitigate with encrypted Postgres volumes (or a managed PG with at-rest encryption) and tight FS / network access. |
| **Audit logging** (CC7.2) | Append-only, hash-chained audit table with actor and workspace per entry; `GET /audit`, `GET /audit/verify`; streaming sink (`OCTOPUS_AUDIT_SINK`); Prometheus auth-event counters. | Covered. **Gap:** retention and the sink's destination are operator-managed; the chain is not signed, so it detects edits but not a full recomputation by a DB superuser (the sink copy covers that). |
| **Change management** (CC8.1) | Idempotent startup schema migrations; image-pull + restart upgrades; backup-before-upgrade tooling. | Covered for the app. CI gates live in the repo; deployment change control is operator-owned. |
| **Availability / resiliency** (A1.2) | Health probes; `restart: unless-stopped`; backup/restore script; Postgres as the durable substrate. | **Gap:** single daemon process and (in the bundled compose) a single Postgres container — no HA / zero-downtime upgrade. Front Postgres with a managed/replicated cluster for tighter RPO. |
| **Secrets management** (CC6.1) | Secrets via env or mounted files; hashes/encryption at rest; one-time API-key display; key TTL cap; secretbox key rotation with re-encryption at start. | Covered. **Gap:** rotating `OCTOPUS_JWT_SECRET` invalidates all sessions (no key-rollover/overlap window). |
| **Vulnerability management** | CI runs gitleaks, semgrep, govulncheck, trivy (`make check-ci`). | Covered in development pipeline. |

> Bottom line for evaluators: the two notable gaps are **no whole-database
> at-rest encryption** and **no HA / online upgrade** (single node). Both are
> mitigable operationally (encrypted volumes, scheduled maintenance windows,
> frequent backups).
