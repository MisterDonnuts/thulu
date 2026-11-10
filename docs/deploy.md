# Deployment Guide

## Prerequisites

- Docker + Docker Compose v2
- A domain pointing to your server (for TLS via Let's Encrypt)
- Ports 80 and 443 open to the internet (for Caddy's ACME challenge)

---

## Quick start

`thulu init` writes `deploy/.env`, picks the MCP servers, starts the stack and
waits for it. That path, the images to mirror for an air-gapped install and
every variable it sets are in [enterprise-setup.md](enterprise-setup.md).

By hand, from a checkout:

```bash
cd thulu/deploy
cp .env.example .env
$EDITOR .env      # the Required variables below; the stack refuses to start without them
docker compose up -d --build
docker compose logs -f
```

---

## Environment variables

### Required

Each compose file refuses to start while one of its required variables is
unset, and names the missing one. `thulu init` writes all of them; by hand,
set the ones your file needs:

| Variable | Needed by | Description |
|----------|-----------|-------------|
| `OCTOPUS_JWT_SECRET` | every stack | Signs sessions and OIDC state; also the fallback key for stored secrets when `OCTOPUS_SECRETBOX_KEY` is unset. Generate with: `openssl rand -hex 32` |
| `OCTOPUS_PG_PASSWORD` | every stack with the bundled Postgres | Password of the bundled Postgres. No default: generate with `openssl rand -hex 24` |
| `OCTOPUS_DOMAIN` | `docker-compose.yml`, `docker-compose.caddy.yml` | Public domain (e.g. `thulu.example.com`), used by Caddy for TLS and for the allowed CORS origin |
| `OCTOPUS_DOCKER_GID` | `docker-compose.yml`, `docker-compose.standalone.yml`, `docker-compose.caddy.yml` | Group that owns the Docker socket on the host (`stat -c %g /var/run/docker.sock`). No default; `0` only if the socket really is root-group |
| `OCTOPUS_API_KEY` | `docker-compose.daemon.yml` (`make up`) | Perimeter gate: every request must present it (see below). Generate with `openssl rand -hex 32` |
| `OCTOPUS_VERSION` | `docker-compose.standalone.yml`, `docker-compose.caddy.yml` | Image tag for the daemon, agent, sandbox and MCP images. These files have no `latest` fallback. `OCTOPUS_IMAGE` replaces only the daemon image; the rest still need `OCTOPUS_VERSION` unless each has its own `OCTOPUS_*_IMAGE` |
| `OCTOPUS_DEV_MODE_CONTAINER` | dev mode inside a container | `1` lets dev mode (`OCTOPUS_DEV_MODE=1`) listen on every interface inside the container, since a loopback bind is unreachable through a published port. Only with the port published on `127.0.0.1`, as `thulu init` does |

`OCTOPUS_DATABASE_URL` is set by every compose file to the bundled Postgres;
set it yourself only to use your own (`postgres://user:pass@host:5432/octopus?sslmode=…`).

### Optional — secrets, TLS and network

| Variable | Description |
|----------|-------------|
| `OCTOPUS_SECRETBOX_KEY` | Recommended. Key (32+ bytes) that encrypts stored LLM keys, credentials and webhook secrets; unset derives it from the JWT secret. Rotation: [security-hardening.md](security-hardening.md#stored-secret-encryption-key-secretbox) |
| `OCTOPUS_DEV_MODE` | `1` turns off logins for local dev: the browser that opens the link `thulu open` prints is the owner, anyone else gets 401 (loopback bind only; refused inside Kubernetes) |
| `OCTOPUS_DEV_TOKEN` | The token that link carries (32+ chars). `thulu init` writes one; unset, the daemon keeps one in its data dir |
| `OCTOPUS_TLS_CERT_FILE` / `OCTOPUS_TLS_KEY_FILE` | Serve HTTPS (TLS 1.2+) from the daemon itself instead of behind a proxy |
| `OCTOPUS_TRUSTED_PROXIES` | Proxies allowed to set `X-Forwarded-For` and `X-Forwarded-Proto` (`private` or IPs/CIDRs) |
| `OCTOPUS_AUDIT_SINK` | Copy every audit entry to `stdout`, `stderr` or `file:/path` |

### Optional — CORS

| Variable | Description |
|----------|-------------|
| `OCTOPUS_ALLOWED_ORIGINS` | Comma-separated list of allowed CORS origins. Default: `https://<OCTOPUS_DOMAIN>` |

### Optional — SSO / OAuth

| Variable | Description |
|----------|-------------|
| `OCTOPUS_OIDC_ISSUER` | OIDC issuer URL: Okta, Microsoft Entra ID, Keycloak, Google (`https://accounts.google.com`). For SAML, put a broker (Keycloak, Entra ID) in front and point this at it |
| `OCTOPUS_OIDC_CLIENT_ID` | OIDC / OAuth client ID |
| `OCTOPUS_OIDC_CLIENT_SECRET` | OIDC / OAuth client secret |
| `OCTOPUS_OIDC_NAME` | Button label on login page (default `SSO`) |
| `OCTOPUS_OIDC_REDIRECT_URI` | Must match: `https://<domain>/auth/oidc/callback` |
| `OCTOPUS_GITHUB_CLIENT_ID` | GitHub OAuth App client ID |
| `OCTOPUS_GITHUB_CLIENT_SECRET` | GitHub OAuth App client secret |
| `OCTOPUS_ALLOW_SSO_AUTOLINK` | `1` lets SSO logins attach to an existing account with the same verified email |
| `OCTOPUS_SSO_ALLOWED_DOMAINS` / `OCTOPUS_SSO_ALLOWED_GROUPS` | Only these email domains / IdP groups may sign in |

### Optional — operator workspace

| Variable | Description |
|----------|-------------|
| `OCTOPUS_OPERATOR_WORKSPACE` | Workspace id whose admins run the install-wide surfaces: the MCP registry, the GitHub App, plans, `/metrics`, and hotfixes to system skills (`PUT /system/skills/{name}`). |

The agent catalog is not one of them: each workspace owns its agents, prompts
and skills, and its own admins edit them (see
[admin-guide.md](admin-guide.md#editing-the-agent-catalog)).

### Optional — sandbox

| Variable | Description |
|----------|-------------|
| `OCTOPUS_SANDBOX_MEMORY_BYTES` | Memory limit for sandbox containers (default `6442450944` = 6 GB). Must be ≤ Docker VM RAM. |
| `OCTOPUS_PUBLIC_HOST` | Hostname used to build `preview_url` values. Defaults to `OCTOPUS_DOMAIN`. |

### Optional — logging

| Variable | Description |
|----------|-------------|
| `OCTOPUS_LOG_FORMAT` | `json` (default in production) or `text`; applies to the request log |

### Optional — EE

| Variable | Description |
|----------|-------------|
| `OCTOPUS_LICENSE_KEY` | Enterprise Edition license key (see [ee-licensing.md](ee-licensing.md)) |
| `OCTOPUS_API_KEY` | Perimeter gate: when set, every request must present it (required by `docker-compose.daemon.yml`). It is **not** an identity — the caller still needs a session or a workspace-scoped key from `POST /auth/api-keys` to reach anything. |

`deploy/.env.example` lists the rest, and [enterprise-setup.md](enterprise-setup.md)
has the full table.

---

## Docker Compose

```yaml
# deploy/docker-compose.yml (simplified view)
services:
  octopus:
    image: octopus:latest
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    restart: unless-stopped
    volumes:
      - octopus-data:/data              # project worktrees, attachments, recent logs
      - /var/run/docker.sock:/var/run/docker.sock      # agents and sandboxes; see the note below
    group_add: ["${OCTOPUS_DOCKER_GID}"]
    expose: ["8585"]
    ports: ["127.0.0.1:8585:8585"]       # loopback only, for thulu init and update
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8585/health/live"]

  caddy:
    image: caddy:2-alpine
    ports: ["80:80", "443:443", "443:443/udp"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
```

Caddy handles TLS (Let's Encrypt automatic cert) and reverse-proxies to `octopus:8585`.

The socket mount is read-write, and `:ro` would not change that: it only stops
the container from replacing the socket file, not from calling the Docker API
through it. Whoever controls the daemon container controls the host's Docker.
The MCP servers are profiles in the same file (`COMPOSE_PROFILES`); Postgres
listens on `127.0.0.1:5433` only (`OCTOPUS_PG_HOST_PORT` moves it), and the daemon on `127.0.0.1:8585` only, where
`thulu init` and `thulu update` wait for it to answer.

---

## Dockerfile (multi-stage)

```
Stage 1: node:22-alpine    — builds Astro frontend (web/dist/)
Stage 2: golang:1.26-alpine — builds Go binary (embeds web/dist/ via go:embed)
Stage 3: alpine:3.21        — runtime (ca-certificates, curl, tzdata only)
```

The final image contains only the `octopus` binary and CA certs. Persistent state lives in Postgres (configured via `OCTOPUS_DATABASE_URL`) and in the `/data` volume (project worktrees, attachments, recent logs).

- Port: `8585`
- Entrypoint: `./octopus serve --data /data`
- Health: `GET /health/live`

---

## Caddy configuration

`deploy/Caddyfile` configures:

- **TLS 1.2/1.3** via Let's Encrypt (automatic cert management)
- **HSTS** with `max-age=31536000; includeSubDomains; preload`
- Security headers: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`
- Server header removed
- No edge rate limiting (the stock caddy image lacks the module); the daemon limits per client IP via `OCTOPUS_RATE_LIMIT_RPS` / `OCTOPUS_RATE_LIMIT_BURST`
- **Reverse proxy** to `octopus:8585` with health checks every 15 seconds
- Structured JSON logging to stdout

```
{$OCTOPUS_DOMAIN} {
    tls { protocols tls1.2 tls1.3 }
    reverse_proxy octopus:8585 {
        health_uri    /health/live
        health_interval 15s
    }
}
```

---

## Data volume

Tables live in Postgres (`OCTOPUS_DATABASE_URL`). Files live in the `/data`
Docker volume:

```
/data/
├── projects/              ← git clones + worktrees
├── task-attachments/      ← task attachment files
├── logs/                  ← run logs
└── runs/                  ← run output directories
```

**Never** delete this volume without a backup.

---

## Backup and restore

Use `deploy/backup.sh`. It writes one `octopus_<timestamp>.tar.gz` holding a
`pg_dump` of the database and the data directory (worktrees and bare repos,
task attachments, usage counters; `logs/` and `runs/` are left out). The
archive is mode 0600.

Tell it where `/data` lives:

```bash
# Compose stack: /data is a named volume, read through a one-off container of
# the daemon's image. Without OCTOPUS_DATABASE_URL the dump runs in the pg
# service.
OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml ./deploy/backup.sh /var/backups/octopus

# A directory on this host.
OCTOPUS_DATABASE_URL=postgres://... OCTOPUS_DATA_DIR=/srv/octopus/data ./deploy/backup.sh

# k3s node (run as root on the node): the chart's local-path PVC, and pg_dump
# inside the in-cluster Postgres pod.
OCTOPUS_BACKUP_SOURCE=kubernetes ./deploy/backup.sh /var/backups/octopus

# Restore (prompts, then overwrites the database and the data directory).
RESTORE=1 OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml \
  ./deploy/backup.sh /var/backups/octopus/octopus_20260522_030000.tar.gz
```

A data directory that is not there, or an empty dump, stops the run with no
archive. `thulu apply` passes its compose file, so its pre-update archive
holds `/data` too.

The newest **5** snapshots are kept (`OCTOPUS_BACKUP_KEEP`), counting these
archives and the updater's `.sql` dumps together; the snapshot of each version
`thulu apply` keeps for going back is never pruned.

### Off-host copy

`OCTOPUS_BACKUP_REMOTE=user@host:dir` copies each archive with rsync over ssh
and keeps the newest `OCTOPUS_BACKUP_REMOTE_KEEP` (default 14) there. The ssh
command defaults to `ssh -o BatchMode=yes` (`OCTOPUS_BACKUP_REMOTE_SSH`); for
another port or key set it to, for example,
`ssh -p 2222 -i /root/.ssh/backup_ed25519 -o BatchMode=yes`. With `OCTOPUS_BACKUP_AGE_RECIPIENT=age1...` the remote copy is
encrypted with [age](https://age-encryption.org); the local one is not. A
failed copy fails the run, so cron and any monitor see it.

The dump holds every stored credential encrypted under
`OCTOPUS_SECRETBOX_KEY`, and not the key. Keep the key somewhere else (a
password manager, printed in a safe): the archive alone exposes none of the
credentials, and without the key a restore cannot read them either.

### Cron example

```cron
0 3 * * * cd /opt/octopus && OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml OCTOPUS_BACKUP_REMOTE=backup@backup.example.com:octopus ./deploy/backup.sh /var/backups/octopus >> /var/log/octopus-backup.log 2>&1
```

---

## Upgrades

`thulu update` and `thulu apply` move an install between published
releases, verify every image and chart first, and roll back a version that
does not come up. The steps, flags, migrations and how to go back are in the
[Upgrade & Migration Guide](upgrade-guide.md).

---

## CLI commands

```bash
# Start the daemon (what the container's entrypoint runs; a daemon on the host is not supported)
./octopus serve --data /data

# Check status
./octopus status

# Tail logs
./octopus logs

# Stop daemon
./octopus stop
```

### `serve` flags

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | `:8585` | Listen address |
| `--agents-dir` | the binary's directory if it has an `agents/` subfolder, else its parent | Base directory: the data dir defaults to a folder under it, and with `OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1` the daemon reads the host LLM key from its `.env`. Agent definitions live in Postgres, not here |
| `--data` | `<agents-dir>/octopus/data` | On-disk data dir (worktrees, attachments, logs) |

---

## Health endpoints

| Endpoint | Auth | Returns |
|----------|------|---------|
| `GET /health/live` | none | 200 always (use for liveness probe) |
| `GET /health/ready` | none | 200 if all DBs healthy, 503 otherwise |
| `GET /health` | none | JSON: daemon version + each DB status |

```json
{
  "status": "ok",
  "version": "1.2.3",
  "checks": {
    "db:tasks": "ok",
    "db:auth": "ok",
    "docker": "ok"
  }
}
```
