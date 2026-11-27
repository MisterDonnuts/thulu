import assert from "node:assert/strict";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";

import { defaultOut, main, parseArgs } from "../lib/cli.js";
import { setLocale } from "../lib/i18n.js";
import { collect, fakeRunner, fakeWhich, isBase, jsonResponse, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const GO_BASE = {
  "go.mod": "module example.com/g1\n\ngo 1.22\n",
  "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
  "calc/calc_test.go": 'package calc\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal("bad")\n\t}\n}\n',
};

const SUB = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int {\n\tif a > b {\n\t\treturn a - b\n\t}\n\treturn -(b - a)\n}\n";

// goHandler fakes `go`: `go test` writes a coverprofile where every line up to `lines` has the
// given count, and reports a passing package; build and vet succeed.
function goHandler({ count = 1, lines = 40, file = "example.com/g1/calc/calc.go", testResult } = {}) {
  return (args) => {
    if (args[0] === "test") {
      const prof = args.find((a) => a.startsWith("-coverprofile="))?.slice("-coverprofile=".length);
      if (prof) writeFileSync(prof, `mode: atomic\n${file}:1.1,${lines}.2 1 ${count}\n`);
      if (testResult) return testResult;
      return { stdout: JSON.stringify({ Action: "pass", Package: "example.com/g1/calc" }) + "\n" };
    }
    return { code: 0 };
  };
}

async function runGates(repo, t, { args = [], handlers = {}, tools = [], fetch = noNetwork } = {}) {
  const out = outDir(t);
  const stdout = collect();
  const stderr = collect();
  const run = fakeRunner(handlers);
  const code = await main(["run", "--quiet", "--out", out, ...args], { run, which: fakeWhich(tools), fetch, cwd: repo.dir, stdout, stderr });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  const check = (id) => report?.checks.find((c) => c.id === id);
  return { code, out, report, check, stdout: stdout.text(), stderr: stderr.text(), calls: run.calls };
}

test("a covered Go change with no findings passes with exit 0 and writes the three artifacts", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const r = await runGates(repo, t, { handlers: { go: goHandler() }, tools: ["go"] });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.report.schema, 1);
  assert.equal(r.report.verdict, "pass");
  assert.deepEqual(r.report.blocking, []);
  assert.deepEqual(r.report.stack, ["go"]);
  assert.match(r.report.base, /^[0-9a-f]{40}$/);
  assert.match(r.report.head, /^[0-9a-f]{40}$/);
  const ids = r.report.checks.map((c) => c.id);
  assert.deepEqual(ids, ["tests", "coverage_new", "duplication", "lint", "typecheck", "sast", "secrets", "deps", "licenses", "phantom_deps", "suppressions", "mutation", "perf"]);
  for (const c of r.report.checks) {
    for (const k of ["id", "tool", "version", "command", "exit_code", "duration_ms", "status", "blocking", "summary", "metrics", "findings", "sarif"]) assert.ok(k in c, `${c.id} lacks ${k}`);
    assert.ok(["pass", "fail", "warn", "skip", "error"].includes(c.status));
  }
  assert.equal(r.check("tests").status, "pass");
  assert.match(r.check("tests").command, /^go test -json/);
  assert.equal(r.check("coverage_new").status, "pass");
  const cov = r.check("coverage_new").metrics;
  assert.ok(cov.new_lines > 0);
  assert.equal(cov.covered_lines, cov.new_lines);
  assert.equal(r.check("sast").status, "skip");
  assert.match(r.check("sast").summary, /semgrep/);
  assert.equal(r.check("mutation").status, "skip");
  assert.ok(existsSync(join(r.out, "report.md")));
  assert.ok(readdirSync(join(r.out, "sarif")).includes("thulu-gates-coverage.sarif"));
  const sarif = JSON.parse(readFileSync(join(r.out, r.check("coverage_new").sarif), "utf8"));
  assert.equal(sarif.version, "2.1.0");
  assert.match(r.stdout, /# thulu-gates: PASS/);
});

test("an unexplained suppression and 20+ uncovered new lines block with exit 1", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  const body = Array.from({ length: 24 }, (_, i) => `\tx += a * ${i + 2}`).join("\n");
  repo.commit({ "calc/big.go": `package calc\n\nfunc Big(a int) int {\n\tx := 0\n${body}\n\treturn x //` + `nolint\n}\n` });
  const r = await runGates(repo, t, { handlers: { go: goHandler({ count: 0, file: "example.com/g1/calc/big.go" }) }, tools: ["go"] });
  assert.equal(r.code, 1);
  assert.equal(r.report.verdict, "fail");
  assert.deepEqual(r.report.blocking.sort(), ["coverage_new", "suppressions"]);
  const cov = r.check("coverage_new");
  assert.equal(cov.metrics.covered_lines, 0);
  assert.ok(cov.metrics.new_lines >= 20);
  const sup = r.check("suppressions").findings[0];
  assert.equal(sup.rule, "suppression-no-rule");
  assert.equal(sup.file, "calc/big.go");
  assert.equal(sup.blocking, true);
  const md = readFileSync(join(r.out, "report.md"), "utf8");
  assert.match(md, /## Blocking findings/);
  assert.match(md, /calc\/big\.go:\d+/);
});

test("--coverage-min lowers the coverage bar from the command line", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  const body = Array.from({ length: 24 }, (_, i) => `\tx += a * ${i + 2}`).join("\n");
  repo.commit({ "calc/big.go": `package calc\n\nfunc Big(a int) int {\n\tx := 0\n${body}\n\treturn x\n}\n` });
  const r = await runGates(repo, t, { args: ["--coverage-min", "0", "--only", "tests,coverage_new"], handlers: { go: goHandler({ count: 0, file: "example.com/g1/calc/big.go" }) }, tools: ["go"] });
  assert.equal(r.code, 0);
  assert.equal(r.check("coverage_new").blocking, false);
  assert.equal(r.check("lint").status, "skip");
  assert.match(r.check("lint").summary, /--only/);
});

test("a failing test blocks and names the test file", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const lines = [
    { Action: "run", Package: "example.com/g1/calc", Test: "TestAdd" },
    { Action: "output", Package: "example.com/g1/calc", Test: "TestAdd", Output: "    calc_test.go:7: bad\n" },
    { Action: "fail", Package: "example.com/g1/calc", Test: "TestAdd" },
    { Action: "fail", Package: "example.com/g1/calc" },
  ];
  const testResult = { code: 1, stdout: lines.map((l) => JSON.stringify(l)).join("\n") + "\n" };
  // It fails on the branch and passes on base: the change broke it.
  const head = goHandler({ testResult });
  const base = goHandler();
  const r = await runGates(repo, t, { args: ["--only", "tests"], handlers: { go: (args, opts) => (isBase(opts) ? base(args, opts) : head(args, opts)) }, tools: ["go"] });
  assert.equal(r.code, 1);
  const f = r.check("tests").findings[0];
  assert.equal(f.file, "calc/calc_test.go");
  assert.equal(f.line, 7);
});

test("a tool that runs out of time is an error (exit 2), not a pass", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const r = await runGates(repo, t, {
    args: ["--only", "tests"],
    handlers: { go: () => ({ code: null, timedOut: true }) },
    tools: ["go"],
  });
  assert.equal(r.code, 2);
  assert.equal(r.report.verdict, "fail");
  assert.deepEqual(r.report.blocking, []);
  assert.equal(r.check("tests").status, "error");
  assert.equal(r.check("tests").metrics.timed_out, true);
});

test("a secret on a new line blocks even without gitleaks; uncommitted files count", async (t) => {
  const repo = makeRepo({ "README.md": "x\n" }, t);
  // Built at runtime so the repo's own secret scanners do not flag this test.
  const key = ["AKIA", "Z7QX", "M2PL", "K9WR", "T4VB"].join("");
  repo.write({ "deploy/env.sh": `export AWS_ACCESS_KEY_ID=${key}\n` });
  const r = await runGates(repo, t, { args: ["--only", "secrets"] });
  assert.equal(r.code, 1);
  assert.equal(r.report.dirty, true);
  const f = r.check("secrets").findings[0];
  assert.equal(f.rule, "aws-access-key-id");
  assert.equal(f.file, "deploy/env.sh");
  assert.equal(f.line, 1);
  assert.equal(f.severity, "critical");
  assert.ok(!readFileSync(join(r.out, "report.md"), "utf8").includes(key));
});

test("semgrep findings count only on added lines; high severity blocks", async (t) => {
  const repo = makeRepo({ "pyproject.toml": '[project]\nname = "a"\n', "app/a.py": "import os\nos.system('ls')\n" }, t);
  repo.commit({ "app/a.py": "import os\nos.system('ls')\neval(input())\n" });
  const semgrep = (args) => {
    if (args[0] === "--version") return { stdout: "1.163.0\n" };
    const out = args.find((a) => a.startsWith("--sarif-output=")).slice("--sarif-output=".length);
    const result = (line, ruleId) => ({ ruleId, level: "error", message: { text: ruleId }, locations: [{ physicalLocation: { artifactLocation: { uri: "app/a.py" }, region: { startLine: line } } }] });
    writeFileSync(
      out,
      JSON.stringify({
        version: "2.1.0",
        runs: [{ tool: { driver: { name: "semgrep", rules: [{ id: "py.eval", properties: { "security-severity": "8.1" } }, { id: "py.system", properties: { "security-severity": "8.1" } }] } }, results: [result(3, "py.eval"), result(2, "py.system")] }],
      }),
    );
    return { code: 0 };
  };
  const r = await runGates(repo, t, { args: ["--only", "sast"], handlers: { semgrep }, tools: ["semgrep"] });
  assert.equal(r.code, 1);
  const sast = r.check("sast");
  assert.equal(sast.version, "1.163.0");
  assert.deepEqual(sast.findings.map((f) => [f.rule, f.line, f.severity]), [["py.eval", 3, "high"]]);
  const call = r.calls.find((c) => c.cmd === "semgrep" && c.args[0] === "scan");
  assert.ok(call.args.includes("--metrics=off"));
  assert.ok(call.args.some((a) => a.startsWith("--baseline-commit=")));
  assert.ok(call.args.includes("p/python"));
});

test("a dependency that does not exist in its registry blocks; an offline registry only warns", async (t) => {
  const repo = makeRepo({ "package.json": JSON.stringify({ name: "x", dependencies: { "left-pad": "1.3.0" } }, null, 2) + "\n" }, t);
  repo.commit({ "package.json": JSON.stringify({ name: "x", dependencies: { "left-pad": "1.3.0", "reqeusts-helper-utils": "1.0.0" } }, null, 2) + "\n" });
  const fetch = async (url) => (url.includes("reqeusts-helper-utils") ? jsonResponse(404, { error: "Not found" }) : jsonResponse(500, ""));
  const r = await runGates(repo, t, { args: ["--only", "phantom_deps"], fetch });
  assert.equal(r.code, 1);
  const f = r.check("phantom_deps").findings[0];
  assert.equal(f.rule, "package-not-found");
  assert.equal(f.file, "package.json");
  assert.ok(f.line > 0);

  const offline = await runGates(repo, t, { args: ["--only", "phantom_deps,licenses"] });
  assert.equal(offline.code, 0);
  assert.equal(offline.check("phantom_deps").status, "warn");
});

test("a recently published package warns, an old one passes, and licenses read the registry", async (t) => {
  const repo = makeRepo({ "package.json": '{"name":"x","dependencies":{}}\n' }, t);
  repo.commit({ "package.json": '{"name":"x","dependencies":{"fresh-pkg":"1.0.0","old-pkg":"2.0.0"}}\n' });
  const now = Date.parse("2026-11-17T00:00:00Z");
  const fetch = async (url) => {
    const fresh = url.includes("fresh-pkg");
    return jsonResponse(200, {
      "dist-tags": { latest: fresh ? "1.0.0" : "2.0.0" },
      time: { created: fresh ? "2026-11-10T00:00:00Z" : "2015-01-01T00:00:00Z" },
      versions: fresh ? { "1.0.0": { license: "MIT" } } : { "2.0.0": { license: "GPL-3.0-only" } },
    });
  };
  const out = outDir(t);
  const code = await main(["run", "--quiet", "--out", out, "--only", "phantom_deps,licenses"], { run: fakeRunner(), which: fakeWhich([]), fetch, now: () => now, cwd: repo.dir, stdout: collect(), stderr: collect() });
  assert.equal(code, 0);
  const report = JSON.parse(readFileSync(join(out, "report.json"), "utf8"));
  const phantom = report.checks.find((c) => c.id === "phantom_deps");
  assert.deepEqual(phantom.findings.map((f) => f.rule), ["recently-published"]);
  const lic = report.checks.find((c) => c.id === "licenses");
  assert.equal(lic.blocking, false);
  assert.deepEqual(lic.findings.map((f) => f.message.includes("old-pkg")), [true]);
});

test("gate config comes from base: a branch cannot lower its own bar, and the change is flagged", async (t) => {
  const repo = makeRepo({ ...GO_BASE, ".thulu/gates.json": '{"coverage_min": 90}\n' }, t);
  repo.commit({ "calc/calc.go": SUB, ".thulu/gates.json": '{"coverage_min": 0}\n' });
  const r = await runGates(repo, t, { args: ["--only", "suppressions,coverage_new,tests"], handlers: { go: goHandler() }, tools: ["go"] });
  assert.equal(r.report.config.coverage_min, 90);
  assert.equal(r.report.config.source, "base");
  const f = r.check("suppressions").findings.find((x) => x.rule === "gate-config-changed");
  assert.equal(f.file, ".thulu/gates.json");
  assert.equal(f.blocking, true);
  assert.equal(r.code, 1);
});

test("--base picks the comparison point; a bad ref is a tool error", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  repo.commit({ "calc/more.go": "package calc\n\nconst K = 1\n" });
  const r = await runGates(repo, t, { args: ["--base", "HEAD~1", "--only", "suppressions"] });
  assert.equal(r.code, 0);
  assert.equal(r.check("suppressions").status, "pass");

  const bad = await runGates(repo, t, { args: ["--base", "no-such-ref"] });
  assert.equal(bad.code, 2);
  assert.match(bad.stderr, /no-such-ref/);
});

test("flag parsing: unknown checks and flags exit 2; --help exits 0", async () => {
  assert.deepEqual(parseArgs(["run", "--only", "lint,sast", "--critical", "a/**", "--critical=b/**", "--dup-max", "5"]).flags.critical, ["a/**", "b/**"]);
  assert.equal(parseArgs(["run", "--dup-max=5"]).flags.dupMax, 5);
  const err = collect();
  assert.equal(await main(["run", "--only", "nope"], { stdout: collect(), stderr: err }), 2);
  assert.match(err.text(), /unknown check "nope"/);
  assert.equal(await main(["run", "--frobnicate"], { stdout: collect(), stderr: collect() }), 2);
  assert.equal(await main(["run", "--coverage-min", "lots"], { stdout: collect(), stderr: collect() }), 2);
  const out = collect();
  assert.equal(await main(["--help"], { stdout: out, stderr: collect() }), 0);
  assert.match(out.text(), /thulu-gates run/);
});

test("doctor lists found tools with versions and missing ones with their install command", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  const run = fakeRunner({ semgrep: () => ({ stdout: "1.163.0\n" }), go: () => ({ stdout: "go version go1.26.3 linux/amd64\n" }) });
  const out = collect();
  const code = await main(["doctor", "--json"], { run, which: fakeWhich(["semgrep", "go"]), cwd: repo.dir, stdout: out, stderr: collect() });
  assert.equal(code, 0);
  const doc = JSON.parse(out.text());
  assert.deepEqual(doc.stacks, ["go"]);
  const byName = Object.fromEntries(doc.tools.map((x) => [x.name, x]));
  assert.equal(byName.semgrep.version, "1.163.0");
  assert.equal(byName.go.version, "1.26.3");
  assert.equal(byName.gitleaks.found, false);
  assert.match(byName.gitleaks.install, /mise use -g github:gitleaks\/gitleaks@8\.30\.1/);
  assert.equal(byName.eslint, undefined);

  const text = collect();
  await main(["doctor"], { run, which: fakeWhich(["semgrep", "go"]), cwd: repo.dir, stdout: text, stderr: collect() });
  assert.match(text.text(), /missing\s+golangci-lint/);
});

test("report.md comes out in Spanish with OCTOPUS_LOCALE=es", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const out = outDir(t);
  try {
    await main(["run", "--quiet", "--out", out, "--only", "suppressions"], { run: fakeRunner(), which: fakeWhich([]), fetch: noNetwork, cwd: repo.dir, stdout: collect(), stderr: collect(), locale: "es" });
  } finally {
    setLocale("en");
  }
  const md = readFileSync(join(out, "report.md"), "utf8");
  assert.match(md, /# thulu-gates: PASA/);
  assert.match(md, /\| Chequeo \|/);
});

test("duplication blocks over dup_max and only lists clones under it", async (t) => {
  const block = Array.from({ length: 24 }, (_, i) => `  total = total + values[${i}] * weight(${i}, "k${i}");`).join("\n");
  const fn = (name) => `export function ${name}(values) {\n  let total = 0;\n${block}\n  return total;\n}\n`;
  const repo = makeRepo({ "src/a.js": fn("a") }, t);
  repo.commit({ "src/b.js": fn("b") });
  const r = await runGates(repo, t, { args: ["--only", "duplication"] });
  assert.equal(r.code, 1);
  const dup = r.check("duplication");
  assert.ok(dup.metrics.percent > 3);
  assert.equal(dup.findings[0].rule, "new-clone");
  assert.equal(dup.findings[0].file, "src/b.js");
  assert.equal(dup.findings[0].blocking, true);

  const filler = Array.from({ length: 400 }, (_, i) => `export const c${i} = ${i} * ${i + 7};`).join("\n");
  const repo2 = makeRepo({ "src/a.js": fn("a") }, t);
  repo2.commit({ "src/b.js": fn("b") + filler + "\n" });
  const r2 = await runGates(repo2, t, { args: ["--only", "duplication", "--dup-max", "10"] });
  assert.equal(r2.code, 0);
  assert.equal(r2.check("duplication").status, "warn");
  assert.equal(r2.check("duplication").findings[0].blocking, false);
});

// A host run's TMPDIR is its own folder: a run without --out must write there, not to the shared
// /tmp of the person's machine.
test("the default out dir is under TMPDIR", () => {
  const prev = process.env.TMPDIR;
  process.env.TMPDIR = "/var/run-root/tmp";
  try {
    const out = defaultOut(Date.UTC(2026, 10, 25, 9, 30, 0));
    assert.ok(out.startsWith("/var/run-root/tmp/thulu-gates/"), out);
  } finally {
    if (prev === undefined) delete process.env.TMPDIR;
    else process.env.TMPDIR = prev;
  }
});
