# Development Guide

## Prerequisites

- **Go 1.26.11** (the version in `go.mod`)
- **Node.js 22.23.4 or later** and npm
- **Docker** with Compose 2.24 or later (Docker Engine, Docker Desktop or Colima)
- **gcc**, for the race detector (`make test` runs with `-race`)
- `git`

Release images are built for linux/amd64 and linux/arm64. Images you build with
`make agent-images` use your machine's architecture.

---

## Repository layout (dev-relevant)

```
thulu/
├── main.go            CLI entry point
├── go.mod             module: github.com/MisterDonnuts/thulu
├── internal/          Go packages (each has its own tests)
├── web/               Astro frontend
│   ├── package.json
│   └── src/
├── deploy/            Dockerfile, docker-compose.yml, Caddyfile
└── ee/                Enterprise features, under their own license (see ee/README.md)
```

---

## Running locally

Thulu runs **only in containers**. The daemon launches agents as sibling
containers over the Docker socket, so a host-process daemon is not supported.

On Colima, point Docker at its socket first:

```bash
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
```

### Run it

```bash
git clone https://github.com/MisterDonnuts/thulu.git && cd thulu
make build-web                                       # web/dist/, which every go run and go build embeds
go run . init -stack deploy/docker-compose.dev.yml   # first time: writes deploy/.env and starts the dev stack
make dev                                             # every time after that
```

`make build-web` runs `npm ci` when `web/node_modules` is missing or older
than the lockfile, then builds the dashboard into `web/dist/`. The Go binary
embeds that directory, so `go run` and `go build` fail without it.

The development stack (`deploy/docker-compose.dev.yml`) runs everything in
containers: `air` rebuilds the daemon on every Go save and serves the API on
`http://localhost:8585`, and the dashboard runs on Vite with hot reload at
`http://localhost:4321`. The setup's menu does not offer this stack: it is for
working on Thulu, not for running it. On first run `GET /auth/config`
reports `has_users: false`; sign in through the configured identity provider
(or the dev-mode link) and the first person in becomes the workspace owner.

### Agent images

Source builds use locally built agent images:

```bash
make agent-images   # sandbox image, base image, then the agent image with the engine CLIs
```

### The built stack

`make up` runs the daemon from an image built out of this checkout (it builds
`web/dist/` inside the image), next to Postgres and the MCP tool sidecars.
`deploy/docker-compose.daemon.yml` needs `OCTOPUS_JWT_SECRET`,
`OCTOPUS_API_KEY` and `OCTOPUS_PG_PASSWORD` in `deploy/.env`
(`openssl rand -hex 32` for each).

```bash
make up      # build + start the full stack; API on http://localhost:8585
make logs    # follow the daemon
make down    # stop (keeps the data + Postgres volumes)
```

---

## Running tests

### Go tests

`make test` is what CI runs: it starts the test Postgres (`make test-pg`) and
runs the suite in three shards in parallel, with `-race`, atomic coverage and a
20-minute timeout. `internal/api` holds about half the test time, so its tests
split into `api-1` and `api-2`; the `rest` shard runs every other package. The
shards come from `scripts/ci/go-test-shard.sh`, which CI calls too, and the
script refuses to run when a test of `internal/api` would land in no shard or
in two.

```bash
make test                     # the three shards in parallel, then coverage.out
GO_TEST_SEQUENTIAL=1 make test  # one after another, on a busy machine
make test-shard SHARD=api-2   # one shard: writes coverage-api-2.out
scripts/ci/go-test-shard.sh list api-1   # the -run pattern of a shard
scripts/ci/go-test-shard.sh check        # every api test in exactly one shard
make coverage-merge           # coverage-<shard>.out → coverage.out
make coverage-gate            # coverage.out ≥ 55%
```

A new test in `internal/api` needs nothing: shards are assigned by name each
run. Each shard writes its own profile; `coverage-merge` joins them into
`coverage.out` with the same totals as a single `go test ./...`.

For a quick loop on one package, plain `go test` works:

```bash
# All tests
go test ./...

# Specific package
go test ./internal/task/...
go test ./internal/auth/...
go test ./internal/sandbox/...

# With race detector
go test -race ./...

# With verbose output
go test -v ./internal/api/...

# Integration tests (require Docker)
go test ./internal/api/ -run Integration -v
go test ./internal/sandbox/ -run E2E -v
```

Test files follow Go conventions (`*_test.go`). Integration tests that require Docker are tagged or use `testing.Short()` to skip in fast mode:

```bash
# Skip integration tests
go test -short ./...
```

### Frontend tests

```bash
cd web
npm test
npm run typecheck  # TypeScript check
```

---

## Rebuilding after changes

Using the live-reload stack (`make dev`)? `air` rebuilds the daemon on every Go
save and Vite hot-reloads the dashboard: nothing to do.

Using the built stack (`make up`)? After a Go or frontend change, rebuild and
restart just the daemon (Postgres and the tool sidecars keep running):

```bash
make restart    # docker compose ... up -d --build octopus
```

The daemon's image builds `web/dist/` itself, so a frontend change is picked up
by the same `make restart`, with no separate `npm run build`.

---

## Adding a new API endpoint

1. Add the handler function to the appropriate `handle_*.go` file in `internal/api/`
2. Register the route in `server.go` with the correct `RequireRole`:
   ```go
   mux.HandleFunc("POST /things/{id}/action", rr(auth.RoleDeveloper, s.handleAction))
   ```
3. Add the test to `api_integration_test.go` or `server_test.go`
4. Add the endpoint to `docs/api-reference.md`

---

## Adding a new metric

1. Declare the metric variable in `internal/metrics/metrics.go`
2. Register it in `Init()` with `registry.MustRegister(...)`
3. Add a `Record*` helper function
4. Call it from business logic
5. Document in `docs/metrics.md`

---

## Managing boards and stages

Workflows are **data, not code**. `internal/stage` owns three tables and which
one owns what is the whole design:

| Table | Holds | Scope |
|-------|-------|-------|
| `boards` | the flows | one per workflow |
| `board_stages` | membership, order, **capability flags** | per (board, stage) |
| `stages` | identity: id, label, colour (its flag columns are only the template applied when a stage joins a new board) | global catalog |

Splitting identity from behavior is what lets `in_progress` auto-advance on one
board and wait for a human on another while a card stays legible when it moves
between flows. Everything is editable at runtime from *Settings → Boards* (or
`/boards` + the `/stages` endpoints with `?board=`), with no rebuild.

Behavior is expressed through capability flags on each column (`initial`,
`terminal`, `auto_advance`, `blocked_by_flags`, `ingest_flags`,
`push_branch`, `open_pr`, `wip_group`) and its approval
settings (`required_approvals`, `on_changes_requested`,
`changes_kick_back_to`). `blocked_by_flags` and `ingest_flags` only matter for
the `flags.yaml` path; the factory board (Backlog → In progress → Done)
sets neither. No flag says whether a column runs an agent: every column but the
initial one and the terminal ones runs the agent the task has assigned there.
The invariants
(exactly one initial, at least one terminal, slug ids `[a-z0-9_]{1,32}`,
flag coherence) are enforced **per board** by `stage.ValidateBoard` on every
write.

### The two interfaces

- `stage.Registry` answers flow questions about **one** board. Get it with
  `boards.Board(task.BoardID)`; never hold a bare Registry for code that
  touches tasks, or two flows will disagree and one of them will be wrong.
- `stage.Boards` resolves registries, plus `Catalog()`: every stage any board
  uses. The catalog is what the stage-keyed configuration surfaces read (agent
  prompts, project hooks, sandbox queue weights), because those are shared
  across boards. Reaching for a board where the catalog belongs is how a stage
  that lives on one flow only becomes unconfigurable.

Both have single-board wrappers (`stage.Static()`, `stage.StaticBoards()`,
`SingleBoard(r)`) so bare construction and tests work without a database.

Code changes are only needed to introduce a **new capability flag**:

1. Add the field to `types.StageInfo` + a column in both `stages` and
   `board_stages` (`stageColumns` in `internal/stage/store.go` covers both)
2. Seed it in `internal/stage/defaults.go` where the built-ins need it
3. Consume it where the behavior lives (dispatcher/API read flags, never ids)
4. Cover it in `stage.ValidateBoard` if it interacts with other flags

> Never compare against a stage id to decide flow behavior — a user board may
> not contain that id at all — and never ask "the" board: ask the task's. The
> virtual stages (`mention`, `chat`) are the only ones fixed in code; they are
> execution modes, not columns, and are appended to every board.

---

## Adding a new EE feature

1. Add a feature constant to `internal/edition/edition.go`.
2. Put the feature's code under `ee/` and connect it to the core through an
   extension point the core declares (a variable or interface `ee/` sets from
   `init`). A gate inside the core can be deleted under the AGPL, so it
   protects nothing on its own.
3. Check `edition.Has(edition.MyFeature)` where the feature runs.
4. Keep `make vet-foss` green: the core must build without `ee/`.
5. Document it in `docs/ee-licensing.md`, and issue licenses that include it.

---

## Linting and formatting

```bash
# Format
gofmt -w ./...

# Vet, as CI runs it (cgo off, test files included)
make vet

# golangci-lint (if installed)
golangci-lint run
```

The lint config is strict about ignored errors. `nolintlint` enforces the
`//nolint` policy (specific linter + explanation + no unused directives). Before
reaching for `//nolint`, read the [error-handling
policy](../CONTRIBUTING.md#error-handling): most cases want `logx.Warn` or the
migration runner, not a suppression.

`make check-ci` runs every CI gate locally with the workflow's flags.

---

## CI pipeline

`.github/workflows/octopus-ci.yml` runs on every PR and on pushes to `main`.
The Go jobs:

- **Go test (api-1, api-2, rest)**: one job per shard, each with its own
  Postgres service and frontend build, running
  `scripts/ci/go-test-shard.sh run <shard>` and uploading its coverage profile.
  About 6 to 8 minutes each.
- **Go test**: passes when all three shards passed. Branch protection looks
  for this job.
- **Coverage gate**: downloads the three profiles, merges them
  (`make coverage-merge`) and requires 55%.
- **Go vet**: `make vet`, which is `CGO_ENABLED=0 go vet ./...`. There is no
  separate `go build` step: vet type-checks every package and test file, and
  with cgo off it also catches code that only compiles with cgo, which is how
  the release binaries are built. golangci-lint skips govet on test files and
  loads packages with cgo on, so it does not cover either case.

`.github/workflows/octopus-release.yml` runs on version tags (`v*`). The
builds start at once and run while `ci-gate` checks that CI passed on the
tagged commit:

1. Cross-compiles the host CLI (`thulu`, no daemon inside) for `linux/amd64`,
   `linux/arm64`, `darwin/amd64` and `darwin/arm64`, as workflow artifacts.
2. Builds the container images, scans them with Trivy and pushes them to
   `ghcr.io` by digest: no tag, no signature, and the jobs hold no OIDC token
   to sign with. They are `linux/amd64` and `linux/arm64`: the platforms live in
   `IMAGE_PLATFORMS` at the top of the workflow, and arm64 builds under QEMU
   (about 18 extra minutes per release, since the runners are amd64).
3. `ci-gate` passes when octopus-ci.yml succeeded on the tag: either a
   `pull_request` run whose head commit has the same tree as the tagged commit
   and ran the compat job (usually the release PR, so the gate passes at
   once), or the `push` run on `main` for that commit, which it waits for. A
   failed or cancelled run never counts.

After the gate, and only then:

4. `sign-images` signs every image digest with cosign keyless and attaches the
   SBOM as a signed attestation (the workflow also publishes SLSA provenance); `chart` pushes and signs the Helm chart.
5. `manifest` builds `octopus-release.json` and `sign` signs it, the images
   and the chart with the KMS release key, then verifies it as a client would.
6. `tag-images` checks each image's keyless signature and points `<version>`
   (and `latest`, for a stable release) at the signed digest, verifying where
   each tag landed.
7. `release` creates the GitHub Release with binaries attached.

---

## Maintainers: cutting a release

```bash
make release VERSION=1.1.0 DRY_RUN=1   # check everything, publish nothing
make release VERSION=1.1.0             # tag and push
```

It refuses to tag unless you are on `main`, the tree is clean, local and
`origin/main` agree, the tag is free, and `CHANGELOG.md` has a `## [1.1.0]`
section: the release workflow reads the release body out of it, so a missing
section publishes empty notes. Then it tags and pushes, and
[the workflow](../.github/workflows/octopus-release.yml) does the rest (see
[CI pipeline](#ci-pipeline)): the images to `ghcr.io` (linux/amd64 and linux/arm64), four binaries with their `.sha256`, an SPDX SBOM, and the GitHub
Release. SLSA provenance attestations are published with it. The CLI binaries cover amd64 and arm64 on
Linux and macOS, and so do the container images. Only the amd64 variant goes
through the Trivy gate: the arm64 one installs the same package versions.

---

## Minting an EE license key (dev)

```bash
go run ./ee/cmd/license keygen        # prints a public and a private key
go build -ldflags "-X github.com/MisterDonnuts/thulu/ee/licensing.VendorPublicKeyHex=<public key hex>" .
OCTOPUS_LICENSE_SIGNING_KEY=<private key hex> go run ./ee/cmd/license \
  -licensee "Dev Instance" -features "*"
```

The binary checks licenses against Mister.D's key, which lives in
`ee/licensing/licensing.go`; the `-X` flag swaps in your own so you can sign a
license for a local build. Set `OCTOPUS_LICENSE_KEY=<output>` when running it.
Tests install a license with `internal/edition/editiontest` instead.

---

## Database schema migrations

Each store owns its schema and applies it through the dependency-free runner in
[`internal/migrate`](../internal/migrate). On startup a store declares an ordered
list of migrations and calls `migrate.Run`:

```go
migrations := []migrate.Migration{
    {ID: 1, SQL: `CREATE TABLE IF NOT EXISTS tasks (...)`},
    {ID: 2, SQL: `ALTER TABLE tasks ADD COLUMN priority TEXT NOT NULL DEFAULT ''`},
}
if err := migrate.Run(context.Background(), db, "task", migrations); err != nil {
    return nil, fmt.Errorf("task schema: %w", err)
}
```

Rules of the runner:

- Migrations run **once**, in ascending `ID` order, recorded in the
  `schema_migrations` table (`component` namespaces each store).
- It **fails loudly** on real errors (syntax, disk, constraints), where a bare
  `db.ExecContext(...) //nolint:errcheck` would swallow them.
- The one benign case it tolerates is re-adding a column that already exists.
- To add a column: append a new `Migration` with the next `ID`. Never edit or
  renumber an applied migration.
- Data backfills that reference a legacy column must be guarded with
  `migrate.ColumnExists` so fresh databases skip them cleanly.

For best-effort runtime errors (not schema), see the error-handling policy in
[CONTRIBUTING.md](../CONTRIBUTING.md#error-handling) and the `internal/logx`
helper.

---

## Database inspection

State lives in Postgres (see `OCTOPUS_DATABASE_URL`). Inspect with `psql`:

```bash
psql "$OCTOPUS_DATABASE_URL" -c "\dt"
psql "$OCTOPUS_DATABASE_URL" -c "SELECT id, title, stage FROM tasks ORDER BY created_at DESC LIMIT 20"
psql "$OCTOPUS_DATABASE_URL" -c "SELECT email, name FROM users"
```

The container-native stack (`make up` or `make dev`) runs a `pg` container that
publishes Postgres on `localhost:5433` (creds `octopus/octopus`). Safe to query
while the daemon is running.

---

## Common development issues

### `web/dist/` not found at build time

Run `make build-web` before `go build` or `go run`. To build or vet the Go
code without Node, an empty placeholder is enough; the daemon then serves a
blank dashboard:

```bash
mkdir -p web/dist && touch web/dist/index.html
```

### Port 8585 already in use

```bash
lsof -i :8585
kill <pid>
```

### Docker socket permission denied (Linux)

```bash
sudo usermod -aG docker $USER
newgrp docker
```

### Colima notes (macOS)

If running Docker via Colima instead of Docker Desktop:

1. **host.docker.internal** is not injected by default. Add `extra_hosts: ["host.docker.internal:host-gateway"]` to any service that needs to reach the host. The `tools/docker-compose.yml` for MCP services already includes this.

2. **VM resources** are configured in `~/.colima/default/colima.yaml`:
   ```yaml
   memory: 12
   cpu: 4
   ```
   `colima start` (without flags) uses these values. Set `OCTOPUS_SANDBOX_MEMORY_BYTES` ≤ VM memory.

### Colima not started (macOS)

```bash
colima start
# Then re-export DOCKER_HOST
export DOCKER_HOST="unix://${HOME}/.colima/default/docker.sock"
```

### Daemon refuses to start with `OCTOPUS_DATABASE_URL is required`

`make up` / `make dev` ship a `pg` service and wire `OCTOPUS_DATABASE_URL` to it
automatically, so this shouldn't happen with the standard stack. If you point at
an external Postgres, set the DSN in `deploy/.env` (see `deploy/.env.example`),
e.g. `OCTOPUS_DATABASE_URL=postgres://user:pass@host:5432/octopus?sslmode=disable`.
