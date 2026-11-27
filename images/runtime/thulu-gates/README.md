# thulu-gates

The quality gates of Thulu's default flow as a single CLI. It compares the working tree against a base commit, runs each check only over what the branch changed, and writes a verdict a verifier agent can read (`report.json`), one a person can read (`report.md`) and one SARIF file per tool. Node 22, built-in modules only, no `npm install`.

It is installed at `/usr/local/bin/thulu-gates` in `images/runtime` and `images/sandbox` (the files live in `/opt/thulu-gates`).

On a runner's machine there is nothing to install: the `thulu` binary carries this CLI (`images.ThuluGates`) and writes it into every job, at `<job folder>/thulu-gates/cli`, with `<job folder>/bin` first on the job's PATH and `THULU_GATES_HOME` naming that copy. Updating `thulu` updates the gates. A copy found earlier on PATH (one a person installed by hand, or a shell profile that prepends `~/.local/bin` again) sees `THULU_GATES_HOME`, hands the run to the job's copy with the same arguments and returns its exit code (`lib/handover.js`); `THULU_GATES_HANDED` stops two copies from handing back and forth. A copy older than this hand-over cannot do it: replace it once, or remove it.

## Usage

```sh
thulu-gates run [--base <ref>] [--out <dir>] [--only a,b] [--skip a,b] [--perf] [--mutation] \
                [--critical <glob>]... [--coverage-min 80] [--dup-max 3] [--quiet] \
                [--cache-dir <dir>] [--quarantine <file>] [--profile <file>] [--reuse]
thulu-gates doctor [--json]
```

- `--base`: the commit to compare against. Default `git merge-base origin/<default> HEAD`, where `<default>` comes from `origin/HEAD` (then `origin/main`, `origin/master`, then the local branch). In a shallow clone it deepens the fetch once by 500 commits. When neither `origin/<default>` nor the local branch exists (a pod clones only the task branch) it exits 2 and asks for `--base`; Thulu gives that commit in the task brief. `typecheck` and `perf` check the base out with `git worktree add`; when the `.git` is read-only (Docker mounts it so) they get `git archive <base>` extracted instead.
- `--out`: default `$TMPDIR/thulu-gates/<YYYYMMDD-HHMMSS>` (`/tmp` when TMPDIR is unset). It can't be the repo or a directory above it. Inside the repo, its files stay out of the diff. A reused `--out` is safe: each tool's previous report is deleted before the tool runs, so a tool that fails without writing reads as an error.
- `--only` / `--skip`: check ids. A check left out shows as `skip` with the reason. `--only mutation` or `--only perf` turns that check on.
- `--mutation`, `--perf`: the two opt-in checks; each costs minutes.
- `--critical <glob>`: files where mutation testing blocks (repeatable, adds to the config's list).
- `--coverage-min`, `--dup-max`: override the thresholds below.
- `--cache-dir`: where base results and the default quarantine live. Then `$THULU_GATES_CACHE`, then `/cache/thulu-gates` when `/cache` is writable (the sandbox's per-workspace cache), then `<tmp>/thulu-gates-cache`. Never inside the repo.
- `--quarantine`: the quarantine file. Then `$THULU_GATES_QUARANTINE`, then `<cache dir>/<project key>/quarantine.json`. See [Base comparison and quarantine](#base-comparison-and-quarantine).
- `--profile`: the project's gate profile. Then `$THULU_GATES_PROFILE`, which Thulu sets on every run of a project that has one. See [Project profile](#project-profile).
- `--reuse` (or `THULU_GATES_REUSE=1`): hand back the results a clean run on this head already stored, and run only what it did not measure. See [Report reuse](#report-reuse).

Uncommitted and untracked files count as part of the change. Progress goes to stderr, `report.md` to stdout.

`thulu-gates doctor` lists the tools each detected stack wants, found (with version) or missing (with the `mise` or package-manager command that installs it). A check whose tool is missing reports `skip` with that same command, never a pass.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | every check passed, warned or was skipped |
| 1 | at least one check has blocking findings |
| 2 | thulu-gates itself failed, a flag was wrong, or a check could not finish (timeout, tool crash) |

`verdict` in `report.json` is `pass` only with exit 0. With exit 2 and no blocking findings, `verdict` is `fail` and `blocking` is empty; `errors` lists the checks that did not finish. A check the project profile accepts unverified reports `accepted` and counts in neither.

## Checks

| id | What it runs | Blocks when |
|---|---|---|
| `tests` | `go test -json -cover`, vitest or jest (from the project's node_modules), Karma (`ng test --watch=false --karma-config=<wrapper>` for Angular, else `karma start <wrapper> --single-run`), `node --test` scripts, `<pm> test`, pytest (+ pytest-cov), `cargo llvm-cov --ignore-run-fail` or `cargo test`, Maven (`-Dmaven.test.failure.ignore=true` with JaCoCo) or Gradle with JaCoCo. Coverage is written even when a test fails, so `coverage_new` still runs | a test the branch broke fails or hangs, or a package does not build. A test that fails or hangs on base too is pre-existing and goes to the quarantine |
| `coverage_new` | the diff crossed with the coverage `tests` left (LCOV, Go coverprofile, Cobertura, JaCoCo) | under `coverage_min`% of new executable lines covered, with at least `coverage_min_lines` new lines; a changed file no report mentions is listed as a warning |
| `duplication` | jscpd if installed (3.x and 4.x get `--gitignore`; 5.x reads `.gitignore` by default and rejects the flag), else a built-in detector (rolling hash over 50-token windows) over the changed files against the repo | more than `dup_max`% of new lines duplicated beyond what each file already had duplicated at base, with at least `coverage_min_lines` new lines; clones under the bar are listed as warnings |
| `lint` | golangci-lint (`--new-from-merge-base`) or `go vet`, gofmt, eslint (with `ESLINT_USE_FLAT_CONFIG=false` when the project has only an `.eslintrc`, which eslint 9 otherwise refuses), prettier, ruff, clippy, rustfmt, checkstyle; lizard for complexity if installed | an error-level finding on a new line that base did not have, a changed file the branch left unformatted (one already unformatted at base is pre-existing), a function that crosses `complexity_max` or grows by 5; a checkstyle violation on a new line when the checkstyle goal failed the build (the project's `violationSeverity` reached) |
| `typecheck` | tsc, pyright or mypy (when configured), `go build`, `cargo check`, Maven/Gradle test compile | a type error base does not have (counted per file, code and message) |
| `sast` | semgrep with `p/default`, the stack's ruleset and the repo's `.semgrep.yml`/`.semgrep/`, `--baseline-commit` on a clean tree | a high or critical finding on a new line |
| `secrets` | gitleaks over the branch's commits plus the uncommitted files; built-in patterns over new lines when gitleaks is missing | any secret |
| `deps` | osv-scanner, govulncheck (only symbols the code calls), npm audit, pip-audit, cargo audit | a high or critical vulnerability with a fix, in a dependency the branch added or bumped (or reached from a changed Go file) |
| `licenses` | the license of each new dependency (node_modules, then the registry, then deps.dev) | a non-dev dependency outside `licenses_allow`, only when the repo sets that list |
| `phantom_deps` | registry lookups over 443 for each new dependency (npm, PyPI, Go proxy, crates.io, Maven Central) | the package or the pinned version does not exist; an unreachable registry only warns |
| `suppressions` | new `nolint`, `eslint-disable`, `noqa`, `type: ignore`, `nosec`, `nosemgrep`, `@ts-ignore`, `#[allow]`, coverage pragmas...; changes to gate configuration | a suppression without the rule it silences or without a reason of 5+ words on the same line, a file-level suppression, or any change to a gate config file (linter configs, `.semgrep*`, `.gitleaks*`, `.thulu/gates.json`, CI workflows, the gate parts of `tsconfig`/`pyproject`/`package.json`) |
| `mutation` | gremlins (`--diff`) or go-mutesting, Stryker (`--incremental --mutate` on changed lines), mutmut (3.x: the changed modules' functions, read with `results --all true`), `cargo mutants --in-diff`, PIT | efficacy under `mutation_min_efficacy`% in `--critical` files; elsewhere survivors and mutants no test reaches are warnings. Efficacy is Stryker's mutation score: killed and timed-out mutants over those plus the survivors and the ones no test reaches. A tool that exits without its results makes the check skip, with the reason in the summary, and lists it as not verified |
| `perf` | Go benchmarks in the changed packages, base and head interleaved over `perf.rounds` rounds (benchstat output saved when installed); or `perf.command` through hyperfine or a built-in timer | a Mann-Whitney U test with p < `perf.alpha` and a median regression over `perf.threshold_pct`% |

Findings outside the lines the branch added are dropped, so a check reports what this change introduced. The repo's own linter configs (`.golangci.yml`, eslint config, `ruff.toml`, `.semgrep.yml`, `.gitleaks.toml`) are used as they are.

Thulu commits whatever a run leaves in the repo, so no tool writes there for long: pytest-cov keeps its data file in `<out>/coverage` (`COVERAGE_FILE`) and Python writes no `__pycache__` (`PYTHONDONTWRITEBYTECODE=1`); Stryker's incremental file goes to `<out>/raw`, its JSON report is copied there, and its `.stryker-tmp` and `reports/mutation` are removed afterwards (a file the project already had is written back); mutmut's `.mutmut-cache` and `mutants/` are removed the same way.

On timeout, a check whose job is to block (`tests`, `lint`, `typecheck`, the security checks) reports `error`, because "not verified" must not read as "passed". `mutation` and `perf` report `skip`.

## Base comparison and quarantine

A check blocks only on what the change makes worse. Where findings can exist before the branch (lint and format, duplication, complexity, typecheck, test failures) thulu-gates runs the same tool on the base commit and compares:

- lint and typecheck findings are matched by file, rule and message (not line, which moves), counting duplicates; a format finding is per file;
- duplication counts, per file, the duplicated lines over what the file had duplicated at base;
- complexity pairs each function with its base version through the diff, so a function whose complexity went down never blocks.

Pre-existing findings stay in the report with `"preexisting": true`, never block, are counted in `metrics.preexisting` and get their own section in `report.md`. When base could not be checked the summary says so and every finding counts as new. Base results are cached under `<cache dir>/<project key>/base/<base sha>/<check>/`, keyed also by tool version, so a second run on the same base does not run them again.

`tests` runs the suite at head first. When tests fail or hang, it runs the suite on base (cached the same way). A test that fails or hangs on base too does not block and is added to the quarantine file; later runs leave it out and report `N quarantined tests not run`. A person deletes an entry to bring a test back. A test hangs when it started and never ended: thulu-gates adds its own reporter to each runner (written to `$THULU_GATES_PROGRESS`) and cuts a suite that prints nothing and reports nothing for `timeouts.test_stall` seconds (default 300, 0 turns it off). It then runs the suite again without the tests that were in flight, up to three times, so the rest is still verified.

A run that did not cover the suite never passes: a time-out with no test to blame, Karma's `Executed N of M` with N under M, a browser that disconnected or ended in `ERROR` (Karma and `ng test` can exit 0 after that), a runner that exits without its report, or no test run at all, all make `tests` an `error` that says "not verified". A base run like that is no baseline: it is not cached and every head failure counts as new. `coverage_new` does not read the partial coverage of such a run and is not verified either.

How each runner leaves one test out:

| Runner | Leaves out | How |
|---|---|---|
| Karma / Jasmine (`ng test`, `karma start`) | one spec | a wrapper config: the project's config plus `client.args: ['--grep', <pattern>]` and a reporter. Needs a karma config file; without one the suite runs once with `--watch=false` and nothing is left out |
| Jest | one test (or one file) | `--testNamePattern` with a negative lookahead; a file through a path pattern |
| Vitest | one test (or one file) | `--testNamePattern`; a file with `--exclude` |
| `node --test` | one test | `--test-skip-pattern` in `NODE_OPTIONS` (Node 22.1+; older Node runs it) |
| pytest | one test | `--deselect <node id>` |
| go test | one top-level test | `-skip '^(?:Name)$'` (Go 1.20+); a failing subtest quarantines its top-level test |
| cargo test | one test | `-- --exact --skip <path>` |
| Maven, Gradle, `<pm> test`, `commands.tests` | nothing | the quarantined tests run, and the report says this runner cannot leave a single test out. `commands.tests` gets `THULU_GATES_QUARANTINE` in its environment |

`report.json` adds `not_verified` (checks that errored, were skipped with `--skip`, or depend on one that did not finish), `preexisting` (count per check), `quarantine` (`file`, `not_run`, `added`, `unsupported`) and `cache`.

## Configuration

Optional `.thulu/gates.json` at the repo root. thulu-gates reads it **from the base commit**, so a branch cannot loosen its own gates; the branch's copy applies only when base has none. Any change to the file is a blocking `suppressions` finding. Flags win over the file.

```json
{
  "coverage_min": 80,
  "coverage_warn": 90,
  "coverage_min_lines": 20,
  "dup_max": 3,
  "dup_min_tokens": 50,
  "complexity_max": 15,
  "mutation_min_efficacy": 70,
  "new_package_days": 30,
  "critical": ["internal/billing/**"],
  "licenses_allow": ["MIT", "Apache-2.0", "BSD-3-Clause", "ISC"],
  "semgrep": { "configs": ["p/default"], "exclude_rules": [] },
  "perf": { "threshold_pct": 10, "rounds": 10, "alpha": 0.05, "command": null },
  "timeouts": { "default": 600, "tests": 1800, "typecheck": 900, "mutation": 600, "perf": 1800, "test_stall": 300 },
  "commands": { "tests": "make test" },
  "coverage_files": ["coverage/lcov.info"]
}
```

`commands.tests` replaces the per-stack test runner with a shell command; `coverage_files` then tells `coverage_new` where that command leaves its reports. `new_package_days` is the age under which a new package is flagged as recently published (warning). Timeouts are in seconds.

`accept_unverified` in this file is ignored with a warning: only the project profile can accept a check.

## Project profile

A project's gate profile lives in Thulu, never in the client repo: a person sets it in the project's settings (`gates_profile` in `PATCH /projects/{id}`; an agent run's key cannot change it). Thulu writes it to a file on every run of the project, in the sandbox and on a runner's machine, and names that file in `$THULU_GATES_PROFILE`.

It takes every key of `.thulu/gates.json` and merges over the repo's file, under the flags (defaults, then `.thulu/gates.json`, then the profile, then flags). A branch cannot touch it, so it may relax the repo's gates: a lower coverage bar, a longer `timeouts.tests`, `timeouts.test_stall: 0`, or a `commands.tests` that runs only the specs a suite can finish. It adds one key of its own:

```json
{
  "timeouts": { "tests": 2400 },
  "accept_unverified": {
    "tests": "Karma disconnects on base after ~18 of 3279 specs (ABC-123); accepted by the owner on 2026-11-24",
    "coverage_new": "depends on tests",
    "mutation": "Stryker cannot start Karma in the sandbox"
  }
}
```

`accept_unverified` maps a check id to the reason a person wrote. When that check ends without a result (an `error`: a time-out, a crash, a browser that disconnected; or a skip that depends on one, like `coverage_new` after an unfinished `tests`, or a mutation tool that exited without its results), it reports `accepted` instead: it keeps its summary, does not block, does not fail the verdict or the exit code, and is listed with the reason in `report.json` (`accepted`) and under its own heading in `report.md`. A check with blocking findings is never accepted, and neither is one that finished: an accepted `tests` that does run to the end still fails on a test the branch broke. An unknown check id or an empty reason is dropped with a warning.

The file holds one profile, or one per project of the run, picked by the repo directory's name; a file with a single entry applies to whatever repo the run is in:

```json
{ "schema": 1, "projects": { "app": { "coverage_min": 60 }, "api": {} } }
```

A missing or unreadable profile is a warning and the run goes on without it, which only makes the gates stricter. `report.json` names the profile it applied in `config.profile`.

Where Thulu puts it: in the sandbox, `/tmp/.thulu/gates-profile.json`, rewritten before a command whenever the profile changed (the sandbox's `/cache` already holds the cache dir). On a runner's machine, `<job folder>/thulu-gates/profile.json`, and `THULU_GATES_CACHE` points at `<runner work dir>/cache/thulu-gates`, which outlives each job, so base results, the quarantine and stored reports carry over between runs. A value the command sets itself wins for the cache; the profile always comes from Thulu.

## Report reuse

A run on a clean tree (no uncommitted or untracked files) stores its check results in `<cache dir>/<project key>/reports/`, keyed by head, base, this tool's version, the effective configuration (repo file, profile and flags; not `accept_unverified`, which is applied to every report again) and the quarantine. With `--reuse`, a run whose key matches does not run those checks again: it copies their results and SARIF into its `--out` and `report.md` opens with "Reused from `<time>`". The verifier uses it on the head Development already measured.

- A check the stored run did not measure (left out by `--only`, skipped with `--skip`, an opt-in nobody asked for) runs now, and the merged result is stored again.
- A check that ended in `error` runs again, unless the profile accepts it: then the stored result is reused and reported `accepted`.
- `--reuse --only a,b` runs `a` and `b` again and keeps every other stored result. Running `coverage_new` again runs `tests` too, because the coverage files are not stored.
- A dirty tree, a new commit, another base or a changed configuration or quarantine runs everything; the reason goes to stderr.

`report.json` adds `reused`: `from` (when the stored run started), `out` (its folder, where its raw output and coverage stay), `checks` (reused) and `rerun`.

## Output

`<out>/report.json` (schema 1, the contract the verifier agent reads):

```json
{
  "schema": 1, "base": "<sha>", "head": "<sha>", "stack": ["go"],
  "verdict": "pass", "blocking": [],
  "checks": [{
    "id": "coverage_new", "tool": "thulu-gates/coverage", "version": "", "command": "",
    "exit_code": null, "duration_ms": 0, "status": "pass", "blocking": false,
    "summary": "100% of new lines covered (5/5)", "metrics": {},
    "findings": [{ "rule": "", "severity": "", "file": "", "line": 0, "message": "", "blocking": false, "fingerprint": "" }],
    "sarif": "sarif/thulu-gates-coverage.sarif"
  }]
}
```

`status` is one of `pass`, `fail`, `warn`, `skip`, `error` and `accepted`. Beyond the contract it adds `errors`, `accepted`, `reused`, `dirty`, `default_branch`, `projects`, `repo_commands`, `declared_versions`, `config` (thresholds in effect and where they came from), `started_at`, `duration_ms` and `out`. Each finding also carries `blocking` and `fingerprint`. `command` holds every command the check ran, joined with ` ; `, ready to paste.

`<out>/sarif/<tool>.sarif` is SARIF 2.1.0, one file per check that ran. Results carry a `partialFingerprints["thuluGates/v1"]` hash of tool, rule, file and the trimmed line text, stable when lines move. Checks thulu-gates computes itself are named `thulu-gates-<check>.sarif`. `<out>/raw/` keeps each tool's own output, `<out>/coverage/` the coverage reports.

`report.md` has the verdict, where its results were reused from, a table of checks, the blocking findings (up to 30 per check), warnings (up to 10 per check), pre-existing findings, quarantined tests, what was not verified, what the profile accepted unverified and why, the benchmark table when `perf` ran, the checks that did not run and why, and the commands to reproduce. It comes out in Spanish with `OCTOPUS_LOCALE=es`, like the daemon.

## Development

```sh
npm test            # from this directory
make test-tools     # from the repo root, as CI runs it
```

Tests build throwaway git repos and replace every external tool with a fake command runner, so they need git and nothing else. They also check that `locales/en.json` and `locales/es.json` have the same keys and placeholders and that every key the code uses exists. `lib/i18n.js` follows the MCP servers' `tools/*/i18n.js`; this package cannot import theirs because each image copies only its own directory.

A check is a module in `lib/checks/` exporting `id` and `run(ctx)`, listed in `lib/checks/index.js` in run order (`tests` before `coverage_new`, which reads its reports).
