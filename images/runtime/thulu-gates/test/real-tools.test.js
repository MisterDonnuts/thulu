// Regressions found running the gates against sample repos with the real tools installed. Every
// fixture under fixtures/real/ is output captured from the tool named in its file name; only
// absolute paths, a hostname, a planted fake key and Surefire's JVM properties were replaced or
// cut. Captured Python files end in .txt so linters and scanners leave them alone.

import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { parseOsv, parsePipAudit } from "../lib/checks/deps.js";
import { spdxAllowed } from "../lib/checks/licenses.js";
import { parseCheckstyle } from "../lib/checks/lint.js";
import { mutmutLines, mutmutModule, parseMutmut3, parsePit } from "../lib/checks/mutation.js";
import { parseHyperfine } from "../lib/checks/perf.js";
import { junitFile, parseCargoTest } from "../lib/checks/tests.js";
import { main } from "../lib/cli.js";
import { setLocale } from "../lib/i18n.js";
import { parseJUnit } from "../lib/parsers.js";
import { createToolbox } from "../lib/tools.js";
import { errorLine, firstLine } from "../lib/util.js";
import { collect, fakeRunner, fakeWhich, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const FIX = join(dirname(new URL(import.meta.url).pathname), "fixtures", "real");
const fixture = (name) => readFileSync(join(FIX, name), "utf8");

function put(path, text) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
}

async function gates(repo, t, { args, handlers, tools }) {
  const out = outDir(t);
  const run = fakeRunner(handlers);
  const code = await main(["run", "--quiet", "--out", out, ...args], { run, which: fakeWhich(tools), fetch: noNetwork, cwd: repo.dir, stdout: collect(), stderr: collect() });
  const report = JSON.parse(readFileSync(join(out, "report.json"), "utf8"));
  return { code, report, check: (id) => report.checks.find((c) => c.id === id), calls: run.calls };
}

const lines = (n, f) => Array.from({ length: n }, (_, i) => f(i + 1)).join("\n") + "\n";

test("jscpd 5 runs without --gitignore, which it rejects, and its report is read", async (t) => {
  const repo = makeRepo({ "src/math.ts": lines(30, (i) => `export const m${i} = ${i};`) }, t);
  repo.commit({ "src/stats.ts": lines(30, (i) => `export const s${i} = ${i};`) });
  // What jscpd 5.4.3 does: it refuses --gitignore (exit 2, no report) and writes
  // jscpd-report.json into --output otherwise.
  const jscpd = (args) => {
    if (args.includes("--version")) return { stdout: "jscpd 5.4.3\n" };
    if (args.includes("--gitignore")) return { code: 2, stderr: fixture("jscpd5-gitignore.err") };
    put(join(args[args.indexOf("--output") + 1], "jscpd-report.json"), fixture("jscpd5-report.json"));
    return { code: 0 };
  };
  const r = await gates(repo, t, { args: ["--only", "duplication"], handlers: { jscpd }, tools: ["jscpd"] });
  const dup = r.check("duplication");
  assert.equal(dup.metrics.jscpd_error, undefined);
  assert.equal(dup.tool, "jscpd");
  // The clone of src/math.ts:11-22 at src/stats.ts:14-25; the package-lock.json one touches no
  // changed file.
  const f = dup.findings.find((x) => x.file === "src/stats.ts");
  assert.ok(f, JSON.stringify(dup.findings));
  assert.equal(f.line, 14);
  assert.match(f.message, /src\/math\.ts/);
  assert.ok(!dup.findings.some((x) => x.file === "package-lock.json"));

  // jscpd 4 still gets the flag: without it 3.x and 4.x scan ignored files.
  const four = await gates(repo, t, {
    args: ["--only", "duplication"],
    handlers: { jscpd: (args) => (args.includes("--version") ? { stdout: "4.0.5\n" } : jscpd(args.filter((a) => a !== "--gitignore"))) },
    tools: ["jscpd"],
  });
  assert.ok(four.calls.some((c) => c.cmd === "jscpd" && c.args.includes("--gitignore")));
});

const MUTMUT_BASE = {
  "pyproject.toml": '[project]\nname = "calc"\nversion = "0.1.0"\n\n[tool.mutmut]\nsource_paths = ["calc"]\n',
  "calc/__init__.py": "",
  "calc/core.py": "def clamp(x: int, lo: int, hi: int) -> int:\n    return max(lo, min(x, hi))\n",
};

function mutmut3({ onRun } = {}) {
  return (args, opts) => {
    if (args[0] === "--version") return { stdout: "mutmut, version 3.8.1\n" };
    if (args[0] === "run") {
      if (onRun) return onRun(args, opts);
      put(join(opts.cwd, "mutants/calc/stats.py"), fixture("mutmut3-mutants-stats.py.txt"));
      put(join(opts.cwd, "mutants/calc/stats.py.spans"), fixture("mutmut3-mutants-stats.py.spans"));
      return { code: 0 };
    }
    if (args[0] === "results") return { stdout: fixture("mutmut3-results-all.txt") };
    return { code: 0 };
  };
}

test("mutmut 3: only the changed module, killed mutants counted, each mutant on its line", async (t) => {
  const repo = makeRepo(MUTMUT_BASE, t);
  repo.commit({ "calc/stats.py": fixture("mutmut3-source-stats.py.txt") });
  const r = await gates(repo, t, { args: ["--only", "mutation", "--mutation"], handlers: { mutmut: mutmut3() }, tools: ["mutmut"] });
  const run = r.calls.find((c) => c.cmd === "mutmut" && c.args[0] === "run");
  assert.deepEqual(run.args, ["run", "calc.stats.x_*", "calc.stats.xǁ*"]);
  assert.ok(r.calls.some((c) => c.cmd === "mutmut" && c.args.join(" ") === "results --all true"));
  const m = r.check("mutation");
  // 54 mutants of calc/stats.py ran: 5 killed, 11 survived, 38 no test reached; the 18 of
  // calc/core.py are "not checked" and the file did not change.
  assert.equal(m.metrics.mutants, 54);
  assert.equal(m.metrics.killed, 5);
  assert.equal(m.metrics.survived, 11);
  assert.equal(m.metrics.no_coverage, 38);
  assert.equal(m.metrics.efficacy, 9.3);
  assert.ok(m.findings.every((f) => f.file === "calc/stats.py" && f.line > 0), JSON.stringify(m.findings.filter((f) => !f.line)));
  const at = (name) => m.findings.find((f) => f.message.endsWith(` ${name}`))?.line;
  assert.equal(at("x_median__mutmut_5"), 9);
  assert.equal(at("x_mean__mutmut_1"), 16);
  assert.equal(at("xǁWindowǁfits__mutmut_1"), 48);
  assert.equal(existsSync(join(repo.dir, "mutants")), false);
});

test("mutmut 3 with no mutable function in the changed files is zero mutants, not a crash", async (t) => {
  const repo = makeRepo(MUTMUT_BASE, t);
  repo.commit({ "calc/settings.py": "LIMIT = 3\n" });
  const nomatch = fixture("mutmut3-run-nomatch.txt");
  const r = await gates(repo, t, {
    args: ["--only", "mutation", "--mutation"],
    handlers: { mutmut: mutmut3({ onRun: () => ({ code: 1, stdout: nomatch }) }) },
    tools: ["mutmut"],
  });
  const m = r.check("mutation");
  assert.equal(m.status, "pass");
  assert.equal(m.metrics.mutants, 0);
});

test("mutmut 3 names: module of a file, results parsing and line mapping", () => {
  assert.equal(mutmutModule("calc/stats.py"), "calc.stats");
  assert.equal(mutmutModule("src/pkg/__init__.py"), "pkg");
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-mutmut-"));
  try {
    put(join(dir, "calc/stats.py"), fixture("mutmut3-source-stats.py.txt"));
    put(join(dir, "mutants/calc/stats.py"), fixture("mutmut3-mutants-stats.py.txt"));
    put(join(dir, "mutants/calc/stats.py.spans"), fixture("mutmut3-mutants-stats.py.spans"));
    const got = parseMutmut3(fixture("mutmut3-results-all.txt"), ["calc/stats.py"], mutmutLines(dir, ["calc/stats.py"]));
    assert.equal(got.length, 54);
    // The mutated line holds what the mutant changed: x_half__mutmut_1 turns "x / 2" into "x * 2".
    const half = got.find((x) => x.mutator === "mutmut x_half__mutmut_1");
    assert.equal(fixture("mutmut3-source-stats.py.txt").split("\n")[half.line - 1].trim(), "return x / 2");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("clippy with --all-targets prints each diagnostic twice; each is one finding", async (t) => {
  const repo = makeRepo({ "Cargo.toml": '[package]\nname = "calc"\nversion = "0.1.0"\nedition = "2021"\n', "src/lib.rs": "pub mod stats;\n" }, t);
  repo.commit({ "src/stats.rs": lines(45, (i) => `// line ${i}`) });
  const cargo = (args) => {
    if (args[0] === "--version") return { stdout: "cargo 1.99.0\n" };
    if (args[0] === "clippy") return { code: 101, stdout: fixture("clippy.json") };
    return { code: 0 };
  };
  const r = await gates(repo, t, { args: ["--only", "lint"], handlers: { cargo }, tools: ["cargo"] });
  const got = r.check("lint").findings.map((f) => `${f.rule}:${f.line}`);
  assert.deepEqual(got.sort(), ["clippy::eq_op:30", "clippy::manual_is_multiple_of:9", "clippy::needless_return:34"]);
  assert.equal(r.check("lint").findings.find((f) => f.rule === "clippy::eq_op").blocking, true);
});

test("checkstyle through Maven: one finding per violation, blocking when the goal failed", () => {
  const got = parseCheckstyle(fixture("mvn-checkstyle.txt"), true);
  assert.equal(got.length, 1);
  assert.equal(got[0].rule, "Indentation");
  assert.equal(got[0].file, "src/main/java/com/example/calc/Stats.java");
  assert.equal(got[0].line, 39);
  assert.equal(got[0].blocking, true);
  // Same output from a build that did not fail (violationSeverity above warning): advisory.
  assert.equal(parseCheckstyle(fixture("mvn-checkstyle.txt"), false)[0].blocking, false);
});

test("PIT descriptions come out with XML entities decoded", () => {
  const got = parsePit(fixture("pit-mutations.xml"));
  assert.equal(got.length, 16);
  assert.ok(got.every((m) => !/&(quot|amp|lt|gt|apos);/.test(m.mutator)), got.map((m) => m.mutator).join("\n"));
});

test("osv-scanner: one finding per vulnerability, aliases folded in", () => {
  const got = parseOsv(JSON.parse(fixture("osv-scanner-rust.json")));
  const smallvec = got.filter((v) => v.package === "smallvec");
  assert.equal(smallvec.length, 1);
  assert.equal(smallvec[0].id, "RUSTSEC-2021-0003");
  assert.deepEqual(smallvec[0].aliases, ["GHSA-43w2-9j62-hq99"]);
  assert.equal(smallvec[0].severity, "critical");
});

test("pip-audit JSON: vulnerable pins with their fix", () => {
  const got = parsePipAudit(JSON.parse(fixture("pip-audit.json")));
  const yaml = got.filter((v) => v.package === "pyyaml");
  assert.ok(yaml.length > 0, JSON.stringify(got.map((v) => v.package)));
  assert.ok(yaml.every((v) => v.version === "5.3" && v.fixed));
});

test("crates.io's slash form of a dual license is an OR", () => {
  assert.equal(spdxAllowed("MIT/Apache-2.0", ["MIT", "Apache-2.0"]), true);
  assert.equal(spdxAllowed("MIT/GPL-3.0", ["Apache-2.0"]), false);
});

test("cargo subcommand binaries are asked their version as cargo runs them", async () => {
  const seen = [];
  const run = async (cmd, args) => {
    seen.push([cmd, ...args].join(" "));
    if (args[0] === "mutants") return { code: 0, stdout: "cargo-mutants 27.1.0\n" };
    if (args[0] === "llvm-cov") return { code: 0, stdout: "cargo-llvm-cov 0.9.1\n" };
    if (cmd.endsWith("mvn")) return { code: 0, stdout: "Apache Maven 3.10.1\n" };
    return { code: 1, stdout: "", stderr: "error: unexpected argument" };
  };
  const tb = createToolbox({ run, which: fakeWhich(["cargo-mutants", "cargo-llvm-cov", "mvn"]), root: "/" });
  assert.equal(await tb.version("cargo-mutants", "/fake/bin/cargo-mutants"), "27.1.0");
  assert.equal(await tb.version("cargo-llvm-cov", "/fake/bin/cargo-llvm-cov"), "0.9.1");
  // A check that names what it ran ("mvn checkstyle") still gets the binary's version.
  assert.equal(await tb.version("mvn checkstyle", "/fake/bin/mvn"), "3.10.1");
  assert.ok(seen.includes("/fake/bin/cargo-mutants mutants --version"));
});

test("summaries quote the line that names the error", () => {
  // mutmut 3 with nothing to mutate: a traceback whose last line is the reason.
  assert.match(firstLine(fixture("mutmut3-run-nomatch.txt")), /^AssertionError: Filtered for specific mutants, but nothing matches/);
  // cargo-llvm-cov prints a note and cargo's progress before the error.
  assert.equal(errorLine(fixture("cargo-llvm-cov-phantom.err")), "error: no matching package named `quantum-left-pad-zz9` found");
  assert.match(errorLine(fixture("mvn-phantom.txt")), /^\[ERROR\] Failed to execute goal on project calc: Could not resolve dependencies/);
});

test("failed tests carry file and line: pytest xunit2, Surefire and libtest", () => {
  const py = parseJUnit(fixture("pytest-junit-broken.xml"));
  assert.deepEqual(
    py.map((f) => [f.name, f.file, f.line]),
    [["tests.test_stats.test_median_even", "tests/test_stats.py", 15]],
  );
  const jv = parseJUnit(fixture("surefire-StatsTest.xml"));
  assert.deepEqual(
    jv.map((f) => [f.name, f.classname, f.line]),
    [["com.example.calc.StatsTest.medianEven", "com.example.calc.StatsTest", 10]],
  );
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-junit-"));
  try {
    put(join(dir, "src/test/java/com/example/calc/StatsTest.java"), "class StatsTest {}\n");
    assert.equal(junitFile(dir, "com.example.calc.StatsTest", ["src/test/java"], [".java"]), "src/test/java/com/example/calc/StatsTest.java");
    assert.equal(junitFile(dir, "com.example.calc.Missing", ["src/test/java"], [".java"]), "");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
  assert.deepEqual(parseCargoTest(fixture("cargo-llvm-cov-test-failure.txt")), [
    { name: "stats::tests::median_even", file: "src/stats.rs", line: 43, message: "assertion `left == right` failed" },
  ]);
});

test("hyperfine 2: the shell is chosen explicitly and schema 2 run times are read", async (t) => {
  // 2 runs of 0.082 s and 0.088 s, as measurements[].time_wall_clock in seconds.
  const times = parseHyperfine(JSON.parse(fixture("hyperfine2-export.json")));
  assert.equal(times.length, 2);
  assert.ok(times.every((ns) => ns > 5e7 && ns < 2e8), String(times));
  assert.deepEqual(parseHyperfine({ results: [{ times: [0.5] }] }), [5e8]);

  const repo = makeRepo({ ".thulu/gates.json": JSON.stringify({ perf: { rounds: 3, command: "python3 work.py" } }), "work.py": "pass\n" }, t);
  repo.commit({ "work.py": "pass  # slower\n" });
  const doc = JSON.parse(fixture("hyperfine2-export.json"));
  const hyperfine = (args) => {
    if (args.includes("--version")) return { stdout: "hyperfine 2.0.0\n" };
    // What hyperfine 2.0.0 does with "cd ... && cmd" and no shell chosen.
    if (!args.includes("--shell")) return { code: 1, stderr: fixture("hyperfine2-shell-syntax.err") };
    const side = args[args.indexOf("-n") + 1];
    const scale = side === "head" ? 2 : 1;
    const out = structuredClone(doc);
    for (const m of out.results[0].measurements) m.time_wall_clock.value *= scale;
    put(args[args.indexOf("--export-json") + 1], JSON.stringify(out));
    return { code: 0 };
  };
  const r = await gates(repo, t, { args: ["--only", "perf"], handlers: { hyperfine }, tools: ["hyperfine"] });
  const perf = r.check("perf");
  assert.equal(perf.status, "fail", perf.summary);
  assert.equal(perf.metrics.benchmarks[0].n, 6);
});

test("Maven: a failing test still reaches jacoco:report, and a broken build is not read from last run's reports", async (t) => {
  const pom = "<project><build><plugins><plugin><artifactId>jacoco-maven-plugin</artifactId></plugin></plugins></build></project>\n";
  const repo = makeRepo({ "pom.xml": pom, ".gitignore": "target/\n", "src/main/java/com/example/calc/Calc.java": "class Calc {}\n" }, t);
  repo.commit({ "src/main/java/com/example/calc/Stats.java": "class Stats {}\n" });
  // A previous run left a failing report behind.
  put(join(repo.dir, "target/surefire-reports/TEST-com.example.calc.StatsTest.xml"), fixture("surefire-StatsTest.xml"));
  const mvn = (args) => {
    if (args.includes("--version")) return { stdout: "Apache Maven 3.10.1\n" };
    return { code: 1, stdout: fixture("mvn-phantom.txt") };
  };
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { mvn }, tools: ["mvn"] });
  const call = r.calls.find((c) => c.cmd === "mvn" && c.args.includes("test"));
  assert.deepEqual(call.args, ["-B", "-Dmaven.test.failure.ignore=true", "test", "jacoco:report"]);
  const findings = r.check("tests").findings;
  assert.equal(findings.length, 1);
  assert.match(findings[0].message, /Could not resolve dependencies/);
});
