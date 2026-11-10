# Contributing to Thulu

Thanks for taking the time to contribute.

## Pull requests

Pull requests to the core are welcome. There is no contributor agreement to
sign: every commit carries a `Signed-off-by` line with your name and the
email of the commit's author, which `git commit -s` adds. With it you certify
the [Developer Certificate of Origin 1.1](https://developercertificate.org/):
you wrote the change, or have the right to submit it, under the project's
license: the AGPL v3.0 only with the additional permission in
[LICENSE-EXCEPTION](LICENSE-EXCEPTION) (see [License](#license)). The CI
checks the line on every pull request from a fork.

`ee/` does not take outside pull requests. Everything there is written and
maintained by Mister.D under its own license; open an issue if something in
`ee/` is broken or missing.

Bug reports with steps to reproduce, questions and ideas go to issues.
Security problems go through [SECURITY.md](SECURITY.md), not issues.

## Quick start

```bash
git clone https://github.com/MisterDonnuts/thulu.git && cd thulu
make build-web       # web/dist/, which every go run and go build embeds
go run . init -stack deploy/docker-compose.dev.yml   # first time
make dev             # after that
```

`deploy/docker-compose.dev.yml` runs everything in containers: air rebuilds the
daemon on save and the dashboard runs on Vite with hot reload at
`http://localhost:4321`. The setup's menu does not offer this stack: it is for
working on Thulu, not for running it. Source builds use locally built agent
images: `make agent-images` builds them. The details,
including tests and the built stack, are in
[docs/development.md](docs/development.md#run-it).

## Development workflow

1. Create a branch off `main`.
2. Make your changes.
3. Install the linters and scanners once with `make install-tools` (it also
   needs `pip3`, for semgrep), then run the full quality check: `make check`
   (it includes `make vet-foss`, which builds the core without `ee/`).
4. Run security checks before important PRs: `make check-security`
5. Open a pull request against `main`.

## Quality gates (enforced by CI)

| Check                                                                                         | Command                                         |
| --------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| Go vet, cgo off (also catches what a build would)                                             | `make vet`                                      |
| Go tests + race detector, in three shards                                                     | `make test`                                     |
| Coverage ≥ 55%, over the merged shards                                                        | `make coverage-gate`                            |
| Go lint (golangci-lint v2)                                                                    | `make lint-go`                                  |
| Go format (goimports)                                                                         | `make fmt-check`                                |
| Frontend lint (ESLint)                                                                        | `make lint-web`                                 |
| Frontend type-check                                                                           | `make typecheck`                                |
| Frontend format (Prettier)                                                                    | `make fmt-check`                                |
| Helm chart and Kubernetes manifests                                                           | `make helm-lint`, `make k8s-lint`               |
| Migrations: locked, and safe for the previous release                                         | `make migrations-check`, `make migrations-lint` |
| Secrets (gitleaks), SAST (semgrep), Go vulnerabilities (govulncheck), filesystem scan (trivy) | `make check-security`                           |
| The previous release and this commit on one database                                          | `make compat`                                   |

All of these run in CI on every PR; `make check-ci` runs them all locally. A
PR cannot be merged if any check fails.

## Code style

- **Go**: follow `gofmt`/`goimports` formatting; lint errors are errors, not warnings.
- **TypeScript/Astro**: Prettier + ESLint with zero-warning policy.
- **Comments**: only when the _why_ is non-obvious. No `// returns the foo` comments.

## Error handling

The linter is strict about ignored errors on purpose: an unhandled error should
be a deliberate, visible decision. Pick the option that fits, in this order:

1. **Handle or return it.** If a caller can act on the error, return it
   (wrapped with `%w`). This is the default.
2. **Best-effort? Log and continue.** For cleanup in an error path, a
   fire-and-forget metadata update, or anything where failure is acceptable but
   still worth a trace, use [`internal/logx`](internal/logx):

   ```go
   logx.Warn(m.queue.Release(taskID), "release slot %s", taskID)
   ```

   This logs the error if non-nil and returns, with no `_ =` and no `//nolint`.

3. **Schema changes go through the migration runner.** Never fire DDL with the
   error discarded (`db.ExecContext(...) //nolint`). Add a versioned
   [`migrate.Migration`](internal/migrate) instead; the runner applies it once,
   records it, and fails loudly on real errors:

   ```go
   migrate.Run(ctx, db, "task", []migrate.Migration{{ID: 7, SQL: `...`}})
   ```

### `//nolint` policy (enforced by `nolintlint`)

`//nolint` is a last resort, allowed **only when there is genuinely nothing to
do** — a type-assertion ok-idiom (`v, _ := x.(T)`), a read with a documented
safe default, or a security annotation you've reviewed. When you do use one:

- name the specific linter — `//nolint:errcheck`, never a bare `//nolint`;
- add an explanation after `//` — _why_ it's safe;
- don't leave it unused — if a config exclusion already covers the case, drop
  the inline directive.

Genuinely non-actionable stdlib calls (e.g. `os.RemoveAll`, `ResponseWriter.Write`)
are excluded centrally in [`.golangci.yml`](.golangci.yml) under
`errcheck.exclude-functions` / `exclusions`, so they don't need an inline
`//nolint` at all.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(sandbox): add priority queue for stage scheduling
fix(auth): prevent token reuse after logout
chore: bump dependencies
docs: clarify backup retention in README
```

## Security

See [SECURITY.md](SECURITY.md) for responsible disclosure. Do not open public issues for security vulnerabilities.

## License

The core is licensed under the [GNU AGPL v3.0 only](LICENSE), with the
additional permission in [LICENSE-EXCEPTION](LICENSE-EXCEPTION) that lets the
core be combined with `ee/` and distributed in one binary. `ee/` has its own
license, in [ee/LICENSE](ee/LICENSE). A contribution to the core is licensed
under the AGPL v3.0 only with that same additional permission, like the rest
of the core, and its copyright stays with its author ([NOTICE](NOTICE)).

Code you did not write can come in only under a license that allows that,
such as MIT, BSD or Apache-2.0, with its notice kept. Code under the GPL,
LGPL or AGPL from another project cannot: its authors have not granted the
additional permission. A pull request that removes or changes the
additional permission is not merged.
