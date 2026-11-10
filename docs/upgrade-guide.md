# Upgrade & Migration Guide

How to upgrade a Thulu deployment safely, what happens to the database
schema, and how to roll back.

Related: [admin-guide.md](admin-guide.md) ·
[enterprise-setup.md](enterprise-setup.md) ·
[security-hardening.md](security-hardening.md) ·
[troubleshooting.md](troubleshooting.md) ·
[deploy.md](deploy.md) · [SECURITY.md](../SECURITY.md#verifying-releases)

---

## The process

The same steps in Docker and in Kubernetes, all run by hand by an operator.
Thulu never changes its own version: Settings → Updates says what is
available and gives the command, and nothing in the dashboard or the API
installs anything.

1. **Look.** Settings → Updates lists the releases newer than yours on the
   channels you allow, the security advisories that affect your version, and
   the exact command for each. The same from a shell:
   `thulu update -check`.
2. **Review.** Download the change package (Settings → Updates, or
   `thulu update -check -version X -o change.md`) for your change board:
   versions, every image digest with the command to verify it, migrations and
   whether going back needs a restore, required variables you are missing,
   deploy files that change, manual steps and the rollback plan.
3. **Apply.** `thulu update -version X`. It writes the target version and
   runs `thulu apply`.
4. **Evidence.** The result goes to the audit log (`system.update_reported`,
   and `system.version_changed` from the daemon when the new version starts),
   to Settings → Updates → History, and to `deploy/.octopus-update.json`.

### Which versions you can jump to

The last two minor versions are supported. Each release says in its manifest
which versions can upgrade to it directly (`upgrade_from`, the current and the
previous minor); anything older goes through the oldest supported minor
first. `apply` refuses a jump the release does not allow.

---

## 1.0.0

Releases before 1.0.0 were private previews and used other names. This section
is for an install that ran one; 1.0.0 renames:

| 0.x | 1.0.0 |
|---|---|
| MCP servers `argus-github`, `argus-browser`, `argus-cloud`, `argus-figma`, `argus-sandbox` | `thulu-github`, `thulu-browser`, `thulu-cloud`, `thulu-figma`, `thulu-sandbox` |
| Images `octopus-mcp-argus-<x>` | `octopus-mcp-<x>` |
| `.env` keys `OCTOPUS_MCP_ARGUS_<X>_IMAGE`, `MCP_ARGUS_<X>_URL` | `OCTOPUS_MCP_<X>_IMAGE`, `MCP_THULU_<X>_URL` |
| Helm values `mcp.argusGithub`, `argusBrowser`, `argusCloud`, `argusFigma` | `mcp.thuluGithub`, `thuluBrowser`, `thuluCloud`, `thuluFigma` |
| Container and agent-definition labels `argus.*` | `thulu.*` |
| Kubernetes labels `octopus.io/*` | `thulu.dev/*` |
| Project directory `.argus` | `.thulu` |

Thulu converts on its own:

- **Database.** Migration `agentstore` 18 rewrites the stored names: agent
  definitions and their history, prompts and skill files, the MCP registry and
  run grants, credential tool policies and decisions, tool-use history,
  automations and settings. It renames only the five servers above: a server
  of your own called `argus-something` keeps its name. The audit log is not
  rewritten, so it keeps the names in use when each event happened. An agent
  or skill installed from the workshop whose files mentioned an old name shows
  there as edited by you.
- **Imports.** A workshop bundle, a `thulu agents import` pack or a
  `thulu import` bundle exported by 0.x is rewritten as it is read.
- **Kubernetes.** At start the daemon relabels the namespace of every
  workspace, its network policies and its worktree and cache claims. A sandbox
  pod without the new labels is replaced the next time the task uses it. Run,
  chat and sandbox pods a 0.x daemon created are deleted once they have ended.
- **Docker Compose.** `thulu apply` runs `docker compose up -d
  --remove-orphans`, which removes the old `argus-*` MCP containers.

Before `thulu update -version 1.0.0` (or `helm upgrade`):

1. Let running tasks and chats finish, or stop them. A 0.x agent keeps running
   after the upgrade, but the new daemon does not track it and its result is
   lost.
2. Docker: rename the keys and server names in `deploy/.env`. Otherwise
   `COMPOSE_PROFILES` names profiles that no longer exist and no MCP server
   starts. With GNU or BSD sed (the original stays in `deploy/.env.bak`):

   ```bash
   sed -E -i.bak \
     -e 's/OCTOPUS_MCP_ARGUS_/OCTOPUS_MCP_/g' \
     -e 's/MCP_ARGUS_(GITHUB|BROWSER|CLOUD|FIGMA)_/MCP_THULU_\1_/g' \
     -e 's/argus-(github|browser|cloud|figma|sandbox)/thulu-\1/g' \
     deploy/.env
   ```

3. Kubernetes: label the namespace Thulu runs in. The tenant network policies
   find the control plane by this label; without it agents cannot reach the
   daemon, the LLM gateway or the MCP servers.

   ```bash
   kubectl label namespace octopus thulu.dev/plane=control
   ```

4. Kubernetes: rename `mcp.argusGithub`, `mcp.argusBrowser`, `mcp.argusCloud`
   and `mcp.argusFigma` in your values file to `mcp.thulu*`. Helm ignores the
   old keys and would deploy the servers with the chart's defaults.

After the upgrade, remove what 0.x left behind. Thulu's cleanup only deletes
what carries its current labels, so it leaves these alone:

```bash
# Docker: containers and images of 0.x
for l in argus.agent argus.sandbox argus.chat argus.browser; do
  docker ps -aq --filter "label=$l" | xargs docker rm -f
done
docker images --format '{{.Repository}}:{{.Tag}}' \
  | grep -E '^argus-|/octopus-mcp-argus-' | xargs docker rmi
# The agent network is recreated with the new label on the next launch.
docker network rm octopus-agents

# Kubernetes: a namespace still without thulu.dev/plane belongs to a deleted
# workspace (the daemon relabels only existing ones). Check, then delete it.
kubectl get namespace -l 'octopus.io/plane=data,!thulu.dev/plane'
```

`xargs` with nothing to read may print a usage error; that is harmless. An
agent built from a build context with `OCTOPUS_BUILD_MISSING_IMAGES=1` was
tagged `argus-<agent>:latest` and is now `octopus-built-<agent>:latest`: it is
rebuilt on the next run.

---

## How migrations work

Thulu stores all state in **Postgres** (`OCTOPUS_DATABASE_URL`). Each store
runs its own migration list at startup through `internal/migrate`; starting a
newer binary against an existing database brings the schema forward. There is
no separate migration step.

Every migration follows **expand/contract**: a release only adds
to the schema in a way the previous release can still read and write, and
removing something (a column, a table, a constraint) waits until a release
whose predecessor already stopped using it. Three CI gates hold the rule:
published migrations never change (`internal/schema/migrations.lock`),
destructive statements need a declared `Contract`, and every change runs the
previous release and the new one in turns against one database.

That is what makes going back one release possible without restoring a
backup: the previous release runs on the new schema. Going back further, or
from a release whose manifest names no `rollback_to`, needs the snapshot taken
before the upgrade.

---

## `thulu update` and `thulu apply`

```bash
thulu update -check                      # what is available; changes nothing
thulu update -check -version 1.2.0 -o change.md   # the change package (.md or .json)
thulu update -version 1.2.0              # install 1.2.0 (sets the version, runs apply)
thulu apply                              # apply the version deploy/.env (or the values file) names
thulu update -rollback                   # back to the version before this one
thulu update -to 1.1.0                   # back to a kept version
thulu update -to 1.0.0 -restore backups/octopus_20261001_101500.sql
thulu update -kept                       # kept versions and the command to go back to each
thulu update -keep 1.1.0                 # never prune 1.1.0; -unkeep undoes it
```

What `apply` does, in order. Any failure before step 5 changes nothing; a
failure after it puts the previous version back on its own.

1. **Verify.** Downloads the release manifest and checks its signature with
   the release key embedded in the binary. Every image and the chart are named
   by digest in it, and each one's signature is checked against the same key
   before it is pulled. Nothing else is trusted: not a tag, not the registry.
2. **Run the target's CLI.** The `thulu` binary of version X applies version
   X: `apply` downloads it, checks its sha256 against the manifest and hands
   over. `-no-fetch-cli` skips this when the binary already is that version.
3. **Preflight.** The jump is allowed (`upgrade_from`); every variable the
   release requires is set; its manual steps are confirmed
   (`-ack-manual-steps`); no deploy file was edited by hand (it stops with the
   diff and points to `docker-compose.override.yml`); there is disk space; no
   other update is running (a lock file).
4. **Back up.** With the bundled Postgres, a `pg_dump` snapshot kept with the
   version it belongs to. With an external database, `apply` cannot reach it:
   `-backup-confirmed` says you took your own.
5. **Change.**
   - **Docker:** pulls every image by its signed digest, rewrites the deploy
     files from the verified deploy bundle, writes the managed block in
     `deploy/.env` (between `# >>> thulu apply` and `# <<<`, every image by
     digest) and recreates the stack. `docker-compose.override.yml`, when it
     exists, is always applied on top.
   - **Kubernetes:** `helm upgrade` with the chart from the manifest, by
     digest, and every image by digest. The install's own values go with it:
     `deploy/values-<release>.yaml` when `thulu init` wrote one, else
     `-values <file>`, or `-reuse-values` to keep the ones the Helm release
     already has. With none of the three the update refuses before it
     changes anything, since helm would reset the release to the chart's
     defaults (SSO, the database DSN and the RuntimeClass included).
6. **Confirm.** Waits for `/health/ready` and checks that `/info` reports the
   new version. An update that restarts into the old image is a failure.
7. **Report.** Writes `deploy/.octopus-update.json` and, when the daemon
   answers, posts the result to its audit log. The post needs an API key of
   the operator workspace with admin role and the `settings` permission
   (`OCTOPUS_API_KEY` or `-api-key-file`; never on the command line). A failed
   post is a warning, never a failed update.

`-registry` (or `OCTOPUS_IMAGE_REGISTRY`) pulls every image from a mirror; the
digests stay the signed ones, so a mirror cannot substitute content.

### Going back

- `thulu update -rollback` (or `-to <the previous version>`) when that
  version is the current release's `rollback_to`: only images and deploy files
  change, the database stays. Adding `-restore <snapshot>` restores it anyway,
  for when you want the data as it was too.
- `-to` any other kept version needs `-restore <snapshot>`: the snapshot taken
  before upgrading from it. Data written since is lost. With an external
  database, restore it yourself and pass `-restore external`.
- `thulu update -kept` lists what can be reached and prints the exact
  command for each, snapshot included. Settings → Updates shows the same.

### Kept versions

A Docker install keeps the images and the snapshot of the last
`OCTOPUS_KEEP_VERSIONS` versions (3 by default), so going back needs no
registry. They are pruned together when a newer one pushes them out; `-keep`
pins one. In Kubernetes, the registry and Helm's release history hold the
previous versions.

### Preview builds

A branch is installed as a preview build, never from a checkout:

```bash
gh workflow run octopus-preview.yml --ref main -f branch=feat/x
OCTOPUS_UPDATE_CHANNELS=stable,preview thulu update -version 1.2.1-dev.gabc1234
```

Previews are signed only by the preview workflow, so the CLI needs `cosign`
in `PATH` to verify them, and the preview channel must be allowed. They have
no compatibility run behind them: going back from one always needs
`-restore`. Keep the channel off in production.

---

## Air-gapped and mirrored installs

On a machine that can reach GitHub and the registry:

```bash
thulu mirror 1.2.0 -to registry.corp.example/octopus -dir /srv/octopus-releases
```

It verifies the release first, then copies every image with its signatures,
the chart (to `<to>/charts`, or `-chart-to`), and writes the signed manifest,
deploy bundle and CLI binaries under `-dir` with a `releases.json` index.
Nothing is re-signed. Serve that directory over HTTPS (or use it as a path)
and point the install at it:

```bash
OCTOPUS_UPDATE_SOURCE=https://releases.corp.example/octopus   # or file:///srv/octopus-releases
OCTOPUS_IMAGE_REGISTRY=registry.corp.example/octopus
```

The install verifies the copies with the key embedded in its binary, exactly
as it would verify the originals. `OCTOPUS_UPDATE_SOURCE=off` stops the daemon
from looking for releases at all.

---

## Back up before upgrading

`apply` takes the snapshot itself: `deploy/backup.sh` with the stack's
compose file (database plus `/data`), or, when that fails, a database-only
`pg_dump` it labels as such. For a backup of your own, or before changing
anything by hand, run the script yourself. It bundles a `pg_dump` with the
data directory (worktrees, attachments, usage counters) into a single
timestamped `.tar.gz` that only its owner can read.

```bash
# Compose stack: /data is read out of the daemon's volume.
OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml ./deploy/backup.sh

# A data directory on this host, to a custom directory.
OCTOPUS_DATA_DIR=/path/to/data ./deploy/backup.sh /mnt/backups
```

A missing data directory stops the run instead of producing a database-only
archive. Snapshots of kept versions are never pruned while their version is
kept. Store a copy **off-host**: `OCTOPUS_BACKUP_REMOTE` copies each archive
with rsync over ssh (see [deploy.md](deploy.md#off-host-copy)); a local
backup does not survive disk loss.

> Recommended cadence: nightly cron plus the snapshot `apply` takes before
> every upgrade.

---

## Restoring by hand

`thulu update -to X -restore <snapshot>` does this. The same by hand:

1. **Stop the daemon** so nothing writes during restore:
   ```bash
   docker compose -f deploy/docker-compose.yml stop octopus
   ```
2. **Restore the backup** into the database and the data directory:
   ```bash
   RESTORE=1 OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml \
     ./deploy/backup.sh /path/to/octopus_YYYYMMDD_HHMMSS.tar.gz
   ```
   (The script prompts for confirmation before overwriting the data directory.)
3. **Put the previous version back** in the managed block of `deploy/.env`
   (`thulu apply` with the old version writes it).
4. **Start it and check:**
   ```bash
   docker compose -f deploy/docker-compose.yml up -d
   curl -sf https://<your-domain>/health/ready
   ```

Restoring overwrites `/data`: anything written after the backup is lost.

---

## No online / zero-downtime upgrade

Thulu runs as a **single daemon process** against a shared Postgres
database. There is currently **no online, rolling, or zero-downtime upgrade
path** and no clustering: the daemon is stateless apart from the Postgres
pool, but the restart still drops in-flight requests. Plan upgrades as a
brief maintenance window: back up → pull/build → restart → verify
`/health/ready`. For minimal downtime, pre-pull/pre-build the image so the
only outage is the container restart itself.
