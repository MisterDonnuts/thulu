// Ways a check could read a failure as a pass, or flag what is not the task's code: a tool that
// dies without a report, a report left by a previous run, the tool's own output inside the repo,
// a missing cargo subcommand, a private module.

import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, lstatSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { main } from "../lib/cli.js";
import { linkNodeModules } from "../lib/checks/typecheck.js";
import { parseNpm } from "../lib/deps.js";
import { setLocale } from "../lib/i18n.js";
import { collect, fakeRunner, fakeWhich, git, jsonResponse, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const GO_BASE = {
  "go.mod": "module example.com/g1\n\ngo 1.22\n",
  "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
};

async function gates(repo, { args = [], handlers = {}, tools = [], fetch = noNetwork, out }) {
  const stdout = collect();
  const stderr = collect();
  const run = fakeRunner(handlers);
  const code = await main(["run", "--quiet", "--out", out, ...args], { run, which: fakeWhich(tools), fetch, cwd: repo.dir, stdout, stderr });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  return { code, report, check: (id) => report?.checks.find((c) => c.id === id), stderr: stderr.text(), calls: run.calls };
}

const emptySarif = JSON.stringify({ version: "2.1.0", runs: [{ tool: { driver: { name: "gitleaks", rules: [] } }, results: [] }] });

function reportPath(args) {
  return args[args.indexOf("--report-path") + 1];
}

test("gitleaks dying without a report, or logging a git error, is an error and not a clean scan", async (t) => {
  const repo = makeRepo({ "README.md": "x\n" }, t);
  repo.commit({ "app.js": "export const a = 1;\n" });
  // A broken .gitleaks.toml: gitleaks logs FTL and exits 1, the same code as "leaks found".
  const fatal = await gates(repo, {
    args: ["--only", "secrets"],
    handlers: { gitleaks: (args) => (args[0] === "version" ? { stdout: "8.30.1\n" } : { code: 1, stderr: "FTL unable to load gitleaks config\n" }) },
    tools: ["gitleaks"],
    out: outDir(t),
  });
  assert.equal(fatal.code, 2);
  assert.equal(fatal.check("secrets").status, "error");
  assert.match(fatal.check("secrets").summary, /unable to load gitleaks config/);

  // A git error inside gitleaks: it still writes an empty report and exits 0.
  const gitErr = await gates(repo, {
    args: ["--only", "secrets"],
    handlers: {
      gitleaks: (args) => {
        if (args[0] === "version") return { stdout: "8.30.1\n" };
        writeFileSync(reportPath(args), emptySarif);
        return { code: 0, stderr: "ERR [git] fatal: ambiguous argument 'x..HEAD'\nINF no leaks found\n" };
      },
    },
    tools: ["gitleaks"],
    out: outDir(t),
  });
  assert.equal(gitErr.code, 2);
  assert.equal(gitErr.check("secrets").status, "error");
});

test("gitleaks scans history even when the net diff is empty (a secret committed, then deleted)", async (t) => {
  const repo = makeRepo({ "README.md": "x\n" }, t);
  repo.commit({ "leak.txt": "token\n" });
  repo.commit({ "leak.txt": null });
  const r = await gates(repo, {
    args: ["--only", "secrets"],
    handlers: {
      gitleaks: (args) => {
        if (args[0] === "version") return { stdout: "8.30.1\n" };
        const result = { ruleId: "generic-api-key", level: "error", message: { text: "leak" }, locations: [{ physicalLocation: { artifactLocation: { uri: "leak.txt" }, region: { startLine: 1 } } }] };
        writeFileSync(reportPath(args), JSON.stringify({ version: "2.1.0", runs: [{ tool: { driver: { name: "gitleaks", rules: [] } }, results: [result] }] }));
        return { code: 1 };
      },
    },
    tools: ["gitleaks"],
    out: outDir(t),
  });
  assert.equal(r.code, 1);
  assert.equal(r.check("secrets").findings[0].rule, "generic-api-key");
});

test("a reused --out never feeds a failed tool the previous run's report", async (t) => {
  const repo = makeRepo({ "pyproject.toml": '[project]\nname = "a"\n', "app/a.py": "x = 1\n" }, t);
  repo.commit({ "app/a.py": "x = 1\ny = 2\n" });
  const out = outDir(t);
  const ok = (args) => {
    if (args[0] === "--version") return { stdout: "1.163.0\n" };
    const p = args.find((a) => a.startsWith("--sarif-output=")).slice("--sarif-output=".length);
    writeFileSync(p, JSON.stringify({ version: "2.1.0", runs: [{ tool: { driver: { name: "semgrep", rules: [] } }, results: [] }] }));
    return { code: 0 };
  };
  const first = await gates(repo, { args: ["--only", "sast"], handlers: { semgrep: ok }, tools: ["semgrep"], out });
  assert.equal(first.check("sast").status, "pass");
  const broken = (args) => (args[0] === "--version" ? { stdout: "1.163.0\n" } : { code: 2, stderr: "invalid config p/nope" });
  const second = await gates(repo, { args: ["--only", "sast"], handlers: { semgrep: broken }, tools: ["semgrep"], out });
  assert.equal(second.code, 2);
  assert.equal(second.check("sast").status, "error");
});

test("semgrep exiting with an error is an error even when it left a SARIF", async (t) => {
  const repo = makeRepo({ "pyproject.toml": '[project]\nname = "a"\n', "app/a.py": "x = 1\n" }, t);
  repo.commit({ "app/a.py": "x = 1\ny = 2\n" });
  const semgrep = (args) => {
    if (args[0] === "--version") return { stdout: "1.163.0\n" };
    const p = args.find((a) => a.startsWith("--sarif-output=")).slice("--sarif-output=".length);
    writeFileSync(p, JSON.stringify({ version: "2.1.0", runs: [] }));
    return { code: 7, stderr: "missing config" };
  };
  const r = await gates(repo, { args: ["--only", "sast"], handlers: { semgrep }, tools: ["semgrep"], out: outDir(t) });
  assert.equal(r.check("sast").status, "error");
});

test("an --out inside the repo stays out of the next run's diff; the repo root itself is refused", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nconst K = 2\n" });
  const out = join(repo.dir, "gates");
  await gates(repo, { args: ["--only", "suppressions"], out });
  const second = await gates(repo, { args: ["--only", "suppressions,duplication"], out });
  assert.equal(second.report.dirty, false);
  assert.equal(second.code, 0);

  const refused = await gates(repo, { args: ["--only", "suppressions"], out: repo.dir });
  assert.equal(refused.code, 2);
  assert.match(refused.stderr, /parent directories/);
});

test("a path with a space keeps its name through the diff, so its lines are read", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/my calc.go": "package calc\n\nfunc Mul(a, b int) int {\n\treturn a * b //" + "nolint\n}\n" });
  const r = await gates(repo, { args: ["--only", "suppressions"], out: outDir(t) });
  assert.equal(r.code, 1);
  assert.equal(r.check("suppressions").findings[0].file, "calc/my calc.go");
});

test("diff prefixes from the user's git config do not change the paths", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  git(repo.dir, "config", "diff.mnemonicPrefix", "true");
  git(repo.dir, "config", "diff.noprefix", "true");
  repo.commit({ "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b } //" + "nolint\n" });
  const r = await gates(repo, { args: ["--only", "suppressions"], out: outDir(t) });
  assert.equal(r.check("suppressions").findings[0].file, "calc/calc.go");
});

test("a missing clippy or rustfmt is a lint error; a warning that mentions 'error' is not a type error", async (t) => {
  const repo = makeRepo({ "Cargo.toml": '[package]\nname = "r"\nversion = "0.1.0"\n', "src/lib.rs": "pub fn a() {}\n" }, t);
  repo.commit({ "src/lib.rs": "pub fn a() {}\npub fn b() { let error = 1; }\n" });
  const cargo = (args) => {
    if (args[0] === "--version") return { stdout: "cargo 1.90.0\n" };
    if (args[0] === "clippy") return { code: 101, stderr: "error: no such command: `clippy`\n" };
    if (args[0] === "fmt") return { code: 101, stderr: "error: no such command: `fmt`\n" };
    if (args[0] === "check") return { code: 0, stderr: "src/lib.rs:2:18: warning: unused variable: `error`\n" };
    return { code: 0 };
  };
  const r = await gates(repo, { args: ["--only", "lint,typecheck"], handlers: { cargo }, tools: ["cargo"], out: outDir(t) });
  assert.equal(r.check("lint").status, "error");
  assert.match(r.check("lint").summary, /no such command/);
  assert.equal(r.check("typecheck").status, "pass");
  assert.equal(r.code, 2);
});

test("a failing perf command under hyperfine is an error, not a benchmark with no samples", async (t) => {
  const repo = makeRepo({ "README.md": "x\n", ".thulu/gates.json": JSON.stringify({ perf: { command: "./bench.sh", rounds: 3 } }) }, t);
  repo.commit({ "README.md": "y\n" });
  const hyperfine = (args) => (args[0] === "--version" ? { stdout: "hyperfine 1.19.0\n" } : { code: 1, stderr: "Command terminated with non-zero exit code: 127" });
  const r = await gates(repo, { args: ["--only", "perf"], handlers: { hyperfine }, tools: ["hyperfine"], out: outDir(t) });
  assert.equal(r.check("perf").status, "error");
  assert.equal(r.code, 2);
});

test("an npm alias is looked up under the package it installs", async (t) => {
  assert.deepEqual(parseNpm('{"dependencies":{"my-lodash":"npm:lodash@4.17.21","@x/y":"npm:@real/y@^2"}}').get("@x/y"), { name: "@x/y", version: "^2", dev: false, registryName: "@real/y" });
  const repo = makeRepo({ "package.json": '{"name":"x","dependencies":{}}\n' }, t);
  repo.commit({ "package.json": '{"name":"x","dependencies":{"my-lodash":"npm:lodash@4.17.21"}}\n' });
  const urls = [];
  const fetch = async (url) => {
    urls.push(url);
    if (url.includes("my-lodash")) return jsonResponse(404, {});
    return jsonResponse(200, { "dist-tags": { latest: "4.17.21" }, time: { created: "2012-04-23T00:00:00Z" }, versions: { "4.17.21": { license: "MIT" } } });
  };
  const r = await gates(repo, { args: ["--only", "phantom_deps,licenses"], fetch, out: outDir(t) });
  assert.equal(r.code, 0, JSON.stringify(r.check("phantom_deps")));
  assert.equal(r.check("phantom_deps").status, "pass");
  assert.ok(urls.every((u) => !u.includes("my-lodash")));
});

test("a private Go module is not a phantom: GOPRIVATE from go env, or a go.sum entry", async (t) => {
  const mod = "module example.com/g1\n\ngo 1.22\n";
  const repo = makeRepo({ ...GO_BASE, "go.mod": mod }, t);
  repo.commit({
    "go.mod": `${mod}\nrequire (\n\tcorp.example/internal/lib v1.2.0\n\tgit.acme.test/team/util v0.3.0\n)\n`,
    "go.sum": "git.acme.test/team/util v0.3.0 h1:abc=\ngit.acme.test/team/util v0.3.0/go.mod h1:def=\n",
  });
  const go = (args) => (args[0] === "env" ? { stdout: "corp.example/*\n\n" } : args[0] === "version" ? { stdout: "go version go1.26.3\n" } : { code: 0 });
  // proxy.golang.org answers 410 for modules it cannot fetch.
  const fetch = async () => jsonResponse(410, "not found");
  const r = await gates(repo, { args: ["--only", "phantom_deps"], handlers: { go }, tools: ["go"], fetch, out: outDir(t) });
  assert.equal(r.code, 0, JSON.stringify(r.check("phantom_deps")));
  assert.deepEqual(r.check("phantom_deps").metrics.private_skipped.sort(), ["corp.example/internal/lib", "git.acme.test/team/util"]);
});

test("a changed file no coverage report mentions is listed and turns a pass into a warning", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Two() int { return 2 }\n", "calc/extra.go": "package calc\n\nfunc Three() int { return 3 }\n" });
  const go = (args) => {
    if (args[0] !== "test") return { code: 0 };
    const prof = args.find((a) => a.startsWith("-coverprofile=")).slice("-coverprofile=".length);
    writeFileSync(prof, "mode: atomic\nexample.com/g1/calc/calc.go:1.1,9.2 1 1\n");
    return { stdout: JSON.stringify({ Action: "pass", Package: "example.com/g1/calc" }) + "\n" };
  };
  const r = await gates(repo, { args: ["--only", "tests,coverage_new"], handlers: { go }, tools: ["go"], out: outDir(t) });
  const cov = r.check("coverage_new");
  assert.equal(cov.status, "warn");
  assert.deepEqual(cov.findings.map((f) => [f.rule, f.file, f.blocking]), [["no-coverage-data", "calc/extra.go", false]]);
  assert.equal(r.code, 0);
});

test("linkNodeModules links the project's packages and the hoisted ones above it", (t) => {
  const root = mkdtempSync(join(tmpdir(), "thulu-gates-nm-"));
  const base = mkdtempSync(join(tmpdir(), "thulu-gates-nm-base-"));
  t.after(() => {
    rmSync(root, { recursive: true, force: true });
    rmSync(base, { recursive: true, force: true });
  });
  for (const d of ["node_modules", "packages/web/node_modules"]) mkdirSync(join(root, d), { recursive: true });
  mkdirSync(join(base, "packages/web"), { recursive: true });
  linkNodeModules(root, base, "packages/web");
  assert.ok(lstatSync(join(base, "packages/web/node_modules")).isSymbolicLink());
  assert.ok(lstatSync(join(base, "node_modules")).isSymbolicLink());
  assert.ok(!existsSync(join(base, "packages/node_modules")));
});

test("a vulnerability counts as new when the scanner spells the PyPI name differently", async (t) => {
  const repo = makeRepo({ "requirements.txt": "flask==3.0.0\n" }, t);
  repo.commit({ "requirements.txt": "flask==3.0.0\ntyping-extensions==4.0.0\n" });
  const osv = {
    results: [
      {
        source: { path: "requirements.txt" },
        packages: [
          {
            package: { name: "typing_extensions", version: "4.0.0", ecosystem: "PyPI" },
            groups: [{ ids: ["GHSA-x"], max_severity: "8.1" }],
            vulnerabilities: [{ id: "GHSA-x", summary: "bad", affected: [{ ranges: [{ events: [{ introduced: "0" }, { fixed: "4.1.0" }] }] }] }],
          },
        ],
      },
    ],
  };
  const scanner = (args) => (args[0] === "--version" ? { stdout: "osv-scanner version: 2.0.0\n" } : { code: 1, stdout: JSON.stringify(osv) });
  const r = await gates(repo, { args: ["--only", "deps"], handlers: { "osv-scanner": scanner }, tools: ["osv-scanner"], out: outDir(t) });
  assert.equal(r.code, 1);
  assert.equal(r.check("deps").findings[0].rule, "GHSA-x");
  assert.equal(r.check("deps").findings[0].blocking, true);
});
