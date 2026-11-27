// Block only on what the change makes worse: findings base already had are reported as
// pre-existing, tests that fail or hang on base go to a quarantine outside the repo, base results
// are cached per base commit, and a run that did not cover the suite never reads as a pass.

import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { escapeJs, exclusionPattern, karmaIncomplete, readProgress } from "../lib/checks/tests.js";
import { pairFunctions } from "../lib/checks/lint.js";
import { main } from "../lib/cli.js";
import { baseLine, parseUnifiedDiff } from "../lib/git.js";
import { setLocale } from "../lib/i18n.js";
import { collect, fakeRunner, fakeWhich, isBase, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const FIX = join(dirname(new URL(import.meta.url).pathname), "fixtures", "real");
const KARMA_DISCONNECTED = readFileSync(join(FIX, "karma6-disconnected.txt"), "utf8");

const GO_BASE = {
  "go.mod": "module example.com/g1\n\ngo 1.22\n",
  "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
};
const SUB = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n";

async function gates(repo, t, { args = [], handlers = {}, tools = [], out = outDir(t) }) {
  const run = fakeRunner(handlers);
  const stderr = collect();
  const code = await main(["run", "--quiet", "--out", out, ...args], { run, which: fakeWhich(tools), fetch: noNetwork, cwd: repo.dir, stdout: collect(), stderr });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  const md = existsSync(join(out, "report.md")) ? readFileSync(join(out, "report.md"), "utf8") : "";
  return { code, report, md, check: (id) => report?.checks.find((c) => c.id === id), calls: run.calls, stderr: stderr.text() };
}

const goEvents = (events) => events.map((e) => JSON.stringify({ Package: "example.com/g1/calc", ...e })).join("\n") + "\n";
const failing = (name) => goEvents([{ Action: "run", Test: name }, { Action: "output", Test: name, Output: "    calc_test.go:7: bad\n" }, { Action: "fail", Test: name }, { Action: "fail" }]);
const passing = goEvents([{ Action: "run", Test: "TestAdd" }, { Action: "pass", Test: "TestAdd" }, { Action: "pass" }]);
const goTestCalls = (calls, base) => calls.filter((c) => c.cmd === "go" && c.args[0] === "test" && isBase(c) === base);

test("a test that fails on base too does not block; it is quarantined and left out of the next run", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const quarantine = join(outDir(t), "q.json");
  const first = await gates(repo, t, { args: ["--only", "tests", "--quarantine", quarantine], handlers: { go: () => ({ code: 1, stdout: failing("TestAdd") }) }, tools: ["go"] });
  assert.equal(first.code, 0, first.stderr);
  const tests = first.check("tests");
  assert.equal(tests.blocking, false);
  assert.equal(tests.findings[0].rule, "test-failure-on-base");
  assert.equal(tests.findings[0].preexisting, true);
  assert.match(tests.summary, /1 pre-existing on base/);
  assert.equal(goTestCalls(first.calls, true).length, 1);
  const saved = JSON.parse(readFileSync(quarantine, "utf8"));
  assert.deepEqual(
    saved.tests.map((e) => [e.id, e.runner, e.reason]),
    [["calc::TestAdd", "go", "fails"]],
  );
  assert.deepEqual(
    first.report.quarantine.added.map((e) => e.id),
    ["calc::TestAdd"],
  );

  const second = await gates(repo, t, { args: ["--only", "tests", "--quarantine", quarantine], handlers: { go: () => ({ stdout: passing }) }, tools: ["go"] });
  assert.equal(second.code, 0);
  const head = goTestCalls(second.calls, false)[0];
  assert.deepEqual(head.args.slice(head.args.indexOf("-skip"), head.args.indexOf("-skip") + 2), ["-skip", "^(?:TestAdd)$"]);
  assert.match(second.check("tests").summary, /1 quarantined test not run/);
  assert.deepEqual(
    second.report.quarantine.not_run.map((e) => e.id),
    ["calc::TestAdd"],
  );
  assert.match(second.md, /Quarantined tests not run/);
});

test("the base suite runs once per base commit: a second run reads it from the cache", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const go = (args, opts) => (isBase(opts) ? { stdout: passing } : { code: 1, stdout: failing("TestAdd") });
  const first = await gates(repo, t, { args: ["--only", "tests"], handlers: { go }, tools: ["go"] });
  assert.equal(first.code, 1);
  assert.match(first.check("tests").findings[0].message, /calc_test.go:7: bad/);
  assert.equal(goTestCalls(first.calls, true).length, 1);
  const second = await gates(repo, t, { args: ["--only", "tests"], handlers: { go }, tools: ["go"] });
  assert.equal(second.code, 1);
  assert.equal(goTestCalls(second.calls, true).length, 0);
  assert.equal(second.check("tests").metrics.base_cached, true);
});

test("a hanging test is cut, named and left out so the rest runs; hanging on base too, it is quarantined", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const hang = goEvents([{ Action: "run", Test: "TestAdd" }, { Action: "pass", Test: "TestAdd" }, { Action: "run", Test: "TestHang" }]);
  const go = (args) => (args.includes("-skip") ? { stdout: passing } : { code: null, timedOut: true, stalled: true, stdout: hang });
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { go }, tools: ["go"] });
  assert.equal(r.code, 0, r.stderr);
  const head = goTestCalls(r.calls, false);
  assert.equal(head.length, 2);
  assert.ok(head[1].args.includes("^(?:TestHang)$"));
  const f = r.check("tests").findings[0];
  assert.equal(f.rule, "test-hang-on-base");
  assert.equal(f.blocking, false);
  assert.deepEqual(
    r.report.quarantine.added.map((e) => [e.id, e.reason]),
    [["calc::TestHang", "hangs"]],
  );
});

test("a test that hangs only on the branch blocks, and the rest of the suite is still verified", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const hang = goEvents([{ Action: "run", Test: "TestHang" }]);
  const go = (args, opts) => (isBase(opts) || args.includes("-skip") ? { stdout: passing } : { code: null, timedOut: true, stalled: true, stdout: hang });
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { go }, tools: ["go"] });
  assert.equal(r.code, 1);
  const tests = r.check("tests");
  assert.equal(tests.status, "fail");
  assert.equal(tests.findings[0].rule, "test-hang");
  assert.match(tests.findings[0].message, /TestHang does not finish on this branch; it finishes on base/);
  assert.equal(r.report.quarantine.added.length, 0);
});

test("a cut run that names no test is an error, and coverage_new does not read its partial coverage", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const go = (args) => {
    const prof = args.find((a) => a.startsWith("-coverprofile="))?.slice("-coverprofile=".length);
    if (prof) writeFileSync(prof, "mode: atomic\nexample.com/g1/calc/calc.go:1.1,9.2 1 1\n");
    return { code: null, timedOut: true, stdout: "" };
  };
  const r = await gates(repo, t, { args: ["--only", "tests,coverage_new"], handlers: { go }, tools: ["go"] });
  assert.equal(r.code, 2);
  assert.equal(r.check("tests").status, "error");
  assert.equal(r.check("coverage_new").status, "skip");
  assert.match(r.check("coverage_new").summary, /not verified/);
  assert.deepEqual(
    r.report.not_verified.map((n) => n.id),
    ["tests", "coverage_new"],
  );
  assert.match(r.md, /## Not verified/);
});

test("--skip tests is listed as not verified, and so is coverage_new", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const r = await gates(repo, t, { args: ["--only", "tests,coverage_new", "--skip", "tests"], tools: ["go"] });
  assert.deepEqual(
    r.report.not_verified.map((n) => n.id),
    ["tests", "coverage_new"],
  );
});

// A JS project with a local binary in node_modules/.bin (gitignored, as installs are).
function jsRepo(t, pkg, extra = {}) {
  const repo = makeRepo({ "package.json": JSON.stringify(pkg), ".gitignore": "node_modules/\n", "src/a.js": "export const a = 1;\n", ...extra }, t);
  return repo;
}

function localBin(repo, name) {
  const p = join(repo.dir, "node_modules", ".bin", name);
  mkdirSync(dirname(p), { recursive: true });
  writeFileSync(p, "#!/bin/sh\n");
  chmodSync(p, 0o755);
}

function progress(opts, lines) {
  const file = opts.env?.THULU_GATES_PROGRESS;
  if (file) writeFileSync(file, lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
}

test("Karma exiting 0 after 18 of 39 tests and a browser disconnect is not verified, never a pass", async (t) => {
  const repo = jsRepo(t, { devDependencies: { karma: "6.4.4" }, scripts: { test: "karma start" } }, { "karma.conf.js": "module.exports = () => {};\n" });
  localBin(repo, "karma");
  repo.commit({ "src/a.js": "export const a = 2;\n" });
  const karma = (args, opts) => {
    if (args[0] === "--version") return { stdout: "6.4.4\n" };
    progress(
      opts,
      Array.from({ length: 18 }, (_, i) => ({ event: "end", name: `Many passes ${i}`, status: "pass" })),
    );
    return { code: 0, stdout: KARMA_DISCONNECTED };
  };
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { karma } });
  assert.equal(r.code, 2);
  const tests = r.check("tests");
  assert.equal(tests.status, "error");
  assert.match(tests.summary, /not verified: \.: Karma ran 18 of 39 tests and the browser disconnected/);
  assert.equal(tests.metrics.not_verified, true);
  const call = r.calls.find((c) => c.cmd === "karma" && c.args[0] === "start");
  assert.ok(call.args.some((a) => a.endsWith("hooks/karma.conf.cjs")));
  assert.ok(call.args.includes("--single-run"));
});

test("`ng test` behind an npm script that stops at 18 of 39 is not verified either", async (t) => {
  const repo = jsRepo(t, { scripts: { test: "ng test" } });
  repo.commit({ "src/a.js": "export const a = 2;\n" });
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { npm: () => ({ code: 0, stdout: KARMA_DISCONNECTED }) }, tools: ["npm"] });
  assert.equal(r.code, 2);
  assert.equal(r.check("tests").status, "error");
  const call = r.calls.find((c) => c.cmd === "npm" && c.args[0] === "test");
  assert.deepEqual(call.args, ["test", "--", "--watch=false"]);
});

test("a base run that did not cover the suite is no baseline: the failure still blocks", async (t) => {
  const repo = jsRepo(t, { devDependencies: { karma: "6.4.4" }, scripts: { test: "karma start" } }, { "karma.conf.js": "module.exports = () => {};\n" });
  localBin(repo, "karma");
  repo.commit({ "src/a.js": "export const a = 2;\n" });
  const fails = { event: "end", name: "Other fails", status: "fail", message: "Expected 1 to be 2." };
  const karma = (args, opts) => {
    if (args[0] === "--version") return { stdout: "6.4.4\n" };
    progress(opts, [fails]);
    if (isBase(opts)) return { code: 0, stdout: KARMA_DISCONNECTED };
    return { code: 1, stdout: "Chrome Headless: Executed 39 of 39 (1 FAILED) (0.1 secs / 0.1 secs)\nTOTAL: 1 FAILED, 38 SUCCESS\n" };
  };
  const r = await gates(repo, t, { args: ["--only", "tests"], handlers: { karma } });
  assert.equal(r.code, 1);
  const f = r.check("tests").findings[0];
  assert.equal(f.blocking, true);
  assert.match(f.message, /the base run did not cover the suite/);
  assert.equal(r.report.quarantine.added.length, 0);
});

test("quarantined tests are left out one by one: jest, pytest and cargo", async (t) => {
  const quarantine = join(outDir(t), "q.json");
  writeFileSync(
    quarantine,
    JSON.stringify({
      schema: 1,
      tests: [
        { id: "src/a.test.js::Suite one hangs forever", project: "", runner: "jest", file: "src/a.test.js", name: "Suite one hangs forever", reason: "hangs" },
        { id: "tests/test_a.py::test_slow", project: "", runner: "pytest", file: "tests/test_a.py", name: "tests/test_a.py::test_slow", reason: "fails" },
        { id: "net::tests::flaky", project: "", runner: "cargo", file: "", name: "net::tests::flaky", reason: "fails" },
      ],
    }),
  );

  const js = jsRepo(t, { devDependencies: { jest: "29.7.0" } });
  localBin(js, "jest");
  js.commit({ "src/a.js": "export const a = 2;\n" });
  const jest = (args) => {
    if (args[0] === "--version") return { stdout: "29.7.0\n" };
    const out = args.find((a) => a.startsWith("--outputFile=")).slice("--outputFile=".length);
    writeFileSync(out, JSON.stringify({ numTotalTests: 2, testResults: [] }));
    return { code: 0 };
  };
  const rj = await gates(js, t, { args: ["--only", "tests", "--quarantine", quarantine], handlers: { jest } });
  assert.equal(rj.code, 0, rj.stderr);
  const jargs = rj.calls.find((c) => c.cmd === "jest" && c.args[0] === "--ci").args;
  const pattern = jargs.find((a) => a.startsWith("--testNamePattern=")).slice("--testNamePattern=".length);
  assert.equal(new RegExp(pattern, "i").test("Suite one hangs forever"), false);
  assert.equal(new RegExp(pattern, "i").test("Suite one passes"), true);
  assert.ok(jargs.some((a) => a.endsWith("hooks/jest-progress.cjs")));
  assert.match(rj.check("tests").summary, /1 quarantined test not run/);

  const py = makeRepo({ "pyproject.toml": '[project]\nname = "a"\n', "app/a.py": "x = 1\n", "tests/test_a.py": "def test_slow():\n    pass\n" }, t);
  py.commit({ "app/a.py": "x = 2\n" });
  const pytest = (args) => {
    if (args[0] === "--version") return { stdout: "pytest 8.3.0\n" };
    const junit = args.find((a) => a.startsWith("--junitxml=")).slice("--junitxml=".length);
    writeFileSync(junit, '<?xml version="1.0"?><testsuites><testsuite tests="1"><testcase classname="tests.test_a" name="test_fast"/></testsuite></testsuites>');
    return { code: 0 };
  };
  const rp = await gates(py, t, { args: ["--only", "tests", "--quarantine", quarantine], handlers: { pytest, python3: () => ({ code: 1 }) }, tools: ["pytest", "python3"] });
  const pargs = rp.calls.find((c) => c.cmd === "pytest" && c.args.includes("-q")).args;
  assert.ok(pargs.includes("--deselect=tests/test_a.py::test_slow"));
  assert.ok(pargs.includes("thulu_gates_progress"));

  const rs = makeRepo({ "Cargo.toml": '[package]\nname = "a"\nversion = "0.1.0"\n', "src/lib.rs": "pub fn a() {}\n" }, t);
  rs.commit({ "src/lib.rs": "pub fn a() {}\npub fn b() {}\n" });
  const cargo = (args) => (args[0] === "--version" ? { stdout: "cargo 1.90.0\n" } : { code: 0, stdout: "test result: ok. 3 passed\n" });
  const rc = await gates(rs, t, { args: ["--only", "tests", "--quarantine", quarantine], handlers: { cargo }, tools: ["cargo"] });
  const cargs = rc.calls.find((c) => c.cmd === "cargo" && c.args[0] === "test").args;
  assert.deepEqual(cargs.slice(cargs.indexOf("--")), ["--", "--exact", "--skip", "net::tests::flaky"]);
});

test("prettier: a file already unformatted on base is pre-existing; one the change unformatted blocks", async (t) => {
  const repo = jsRepo(t, { devDependencies: { prettier: "3.3.0" } }, { ".prettierrc": "{}\n", "src/old.js": "BAD\nconst a = 1;\n", "src/ok.js": "const b = 1;\n" });
  localBin(repo, "prettier");
  repo.commit({ "src/old.js": "BAD\nconst a = 2;\n", "src/ok.js": "BAD\nconst b = 1;\n", "src/new.js": "BAD\n" });
  const prettier = (args, opts) => {
    if (args[0] === "--version") return { stdout: "3.3.0\n" };
    const files = args.filter((a) => !a.startsWith("--"));
    return { code: 1, stdout: files.filter((f) => readFileSync(join(opts.cwd, f), "utf8").includes("BAD")).join("\n") + "\n" };
  };
  const r = await gates(repo, t, { args: ["--only", "lint"], handlers: { prettier } });
  assert.equal(r.code, 1);
  const byFile = Object.fromEntries(r.check("lint").findings.map((f) => [f.file, f]));
  assert.equal(byFile["src/old.js"].blocking, false);
  assert.equal(byFile["src/old.js"].preexisting, true);
  assert.match(byFile["src/old.js"].message, /already so on base/);
  assert.equal(byFile["src/ok.js"].blocking, true);
  assert.equal(byFile["src/new.js"].blocking, true);
  assert.equal(r.check("lint").metrics.preexisting, 1);
  const baseCalls = (calls) => calls.filter((c) => c.cmd === "prettier" && c.args[0] === "--list-different" && isBase(c));
  assert.equal(baseCalls(r.calls).length, 1);
  assert.deepEqual(
    baseCalls(r.calls)[0]
      .args.filter((a) => !a.startsWith("--"))
      .sort(),
    ["src/ok.js", "src/old.js"],
  );

  const again = await gates(repo, t, { args: ["--only", "lint"], handlers: { prettier } });
  assert.equal(baseCalls(again.calls).length, 0);
  assert.equal(again.check("lint").findings.filter((f) => f.preexisting).length, 1);
});

test("eslint: an old finding on an edited line is pre-existing; a second copy of it is new", async (t) => {
  const repo = jsRepo(t, { devDependencies: { eslint: "9.0.0" } }, { ".eslintrc.json": "{}\n", "src/a.js": "var x = 1;\nexport { x };\n" });
  localBin(repo, "eslint");
  repo.commit({ "src/a.js": "var x = 2;\nvar y = 3;\nexport { x, y };\n" });
  const eslint = (args, opts) => {
    if (args[0] === "--version") return { stdout: "v9.0.0\n" };
    const files = args.filter((a) => !a.startsWith("--") && a !== "json");
    return {
      code: 1,
      stdout: JSON.stringify(
        files.map((f) => ({
          filePath: join(opts.cwd, f),
          messages: readFileSync(join(opts.cwd, f), "utf8")
            .split("\n")
            .map((l, i) => (l.startsWith("var ") ? { ruleId: "no-var", severity: 2, line: i + 1, message: "Unexpected var, use let or const instead." } : null))
            .filter(Boolean),
        })),
      ),
    };
  };
  const r = await gates(repo, t, { args: ["--only", "lint"], handlers: { eslint } });
  assert.equal(r.code, 1);
  const f = r.check("lint").findings;
  assert.equal(f.length, 2);
  assert.equal(f.filter((x) => x.blocking).length, 1);
  assert.equal(f.filter((x) => x.preexisting).length, 1);
});

test("eslint: a repo with only an .eslintrc runs eslint 9 in legacy-config mode, on head and on base", async (t) => {
  delete process.env.ESLINT_USE_FLAT_CONFIG;
  const seen = [];
  // eslint 9 aborts on an .eslintrc unless ESLINT_USE_FLAT_CONFIG=false.
  const eslint = (args, opts) => {
    if (args[0] === "--version") return { stdout: "v9.39.4\n" };
    seen.push(opts.env?.ESLINT_USE_FLAT_CONFIG);
    if (opts.env?.ESLINT_USE_FLAT_CONFIG !== "false" && !existsSync(join(opts.cwd, "eslint.config.js"))) {
      return { code: 2, stderr: "ESLint couldn't find an eslint.config.(js|mjs|cjs) file.\n" };
    }
    const files = args.filter((a) => !a.startsWith("--") && a !== "json");
    return { code: 1, stdout: JSON.stringify(files.map((f) => ({ filePath: join(opts.cwd, f), messages: [{ ruleId: "no-var", severity: 2, line: 1, message: "Unexpected var." }] }))) };
  };
  const legacy = jsRepo(t, { devDependencies: { eslint: "9.39.4" } }, { ".eslintrc.json": "{}\n", "src/a.js": "var x = 1;\nexport { x };\n" });
  localBin(legacy, "eslint");
  legacy.commit({ "src/a.js": "var x = 2;\nexport { x };\n" });
  const r = await gates(legacy, t, { args: ["--only", "lint"], handlers: { eslint } });
  assert.notEqual(r.check("lint").status, "error", JSON.stringify(r.check("lint")));
  assert.deepEqual(seen, ["false", "false"], "head and base both run with the legacy config");
  assert.equal(r.check("lint").findings.filter((f) => f.preexisting).length, 1);

  seen.length = 0;
  const flat = jsRepo(t, { devDependencies: { eslint: "9.39.4" } }, { ".eslintrc.json": "{}\n", "eslint.config.js": "export default [];\n", "src/a.js": "var x = 1;\nexport { x };\n" });
  localBin(flat, "eslint");
  flat.commit({ "src/a.js": "var x = 2;\nexport { x };\n" });
  await gates(flat, t, { args: ["--only", "lint"], handlers: { eslint } });
  assert.ok(seen.length > 0 && seen.every((v) => v === undefined), `a flat config runs eslint as is: ${seen}`);
});

test("duplication: blocks that were copies on base and were edited the same way do not count as new", async (t) => {
  const block = (word) => Array.from({ length: 30 }, (_, i) => `export const v${i} = compute(${i}, "${i < 25 ? word : "x"}", [1, 2, 3]);`).join("\n") + "\n";
  const repo = makeRepo({ "src/a.ts": `// a\n${block("x")}`, "src/b.ts": `// b\n${block("x")}` }, t);
  repo.commit({ "src/a.ts": `// a\n${block("y")}`, "src/b.ts": `// b\n${block("y")}` });
  const r = await gates(repo, t, { args: ["--only", "duplication"] });
  assert.equal(r.code, 0, r.stderr);
  const dup = r.check("duplication");
  assert.notEqual(dup.status, "fail");
  assert.equal(dup.metrics.percent, 0);
  assert.equal(dup.metrics.percent_with_base, 100);
  assert.ok(dup.metrics.preexisting > 0);
  assert.ok(dup.findings.every((f) => f.preexisting && f.rule === "clone-on-base"));
});

test("complexity: a function whose complexity went down is never flagged, even among anonymous ones", async (t) => {
  const lines = (n) => Array.from({ length: n }, (_, i) => `  // line ${i + 1}`).join("\n") + "\n";
  const repo = makeRepo({ "package.json": "{}\n", "src/a.ts": lines(40) }, t);
  repo.commit({ "src/a.ts": lines(40).replace("// line 5\n", "// line 5 edited\n") });
  const csv = (file, rows) => rows.map(([ccn, start, end]) => `10,${ccn},50,1,${end - start + 1},"(anonymous)@${start}-${end}@${file}","${file}","(anonymous)","(anonymous)( x )",${start},${end}`).join("\n") + "\n";
  const lizard = (args, opts) => {
    if (args[0] === "--version") return { stdout: "1.17.10\n" };
    // Base: the edited function (lines 2-20) at 30, a small one after it at 3. Head: 27.
    return { stdout: /thulu-gates-lizard-/.test(opts.cwd) ? csv("src/a.ts", [[30, 2, 20], [3, 30, 35]]) : csv("src/a.ts", [[27, 2, 20], [3, 30, 35]]) };
  };
  const r = await gates(repo, t, { args: ["--only", "lint"], handlers: { lizard }, tools: ["lizard"] });
  assert.equal(r.code, 0, r.stderr);
  const f = r.check("lint").findings;
  assert.equal(f.filter((x) => x.blocking).length, 0);
  assert.equal(f.length, 1);
  assert.equal(f[0].preexisting, true);
  assert.match(f[0].message, /it was 30 on base/);
});

test("typecheck: an error base already had in a changed file is pre-existing, not new", async (t) => {
  const repo = jsRepo(t, { devDependencies: { typescript: "5.6.0" } }, { "tsconfig.json": "{}\n", "src/a.ts": "const a: number = 'x';\nexport const b = 1;\n" });
  localBin(repo, "tsc");
  repo.commit({ "src/a.ts": "const a: number = 'x';\nexport const b = 2;\n" });
  const tsc = (args) => (args[0] === "--version" ? { stdout: "Version 5.6.0\n" } : { code: 2, stdout: "src/a.ts(1,7): error TS2322: Type 'string' is not assignable to type 'number'.\n" });
  const r = await gates(repo, t, { args: ["--only", "typecheck"], handlers: { tsc } });
  assert.equal(r.code, 0, r.stderr);
  const tc = r.check("typecheck");
  assert.equal(tc.blocking, false);
  assert.equal(tc.findings[0].preexisting, true);
  assert.match(tc.summary, /1 pre-existing on base/);
});

test("karmaIncomplete reads a disconnect, a short count and nothing run; a whole run is complete", () => {
  assert.deepEqual(karmaIncomplete(KARMA_DISCONNECTED), { reason: "disconnected", executed: 18, total: 39 });
  assert.equal(karmaIncomplete("HeadlessChrome: Executed 39 of 39 SUCCESS (1 sec / 0.9 secs)\nTOTAL: 39 SUCCESS\n"), null);
  assert.equal(karmaIncomplete("Executed 2 of 3 (1 FAILED) (skipped 1) (0.007 secs / 0.001 secs)\nTOTAL: 1 FAILED, 1 SUCCESS\n"), null);
  assert.equal(karmaIncomplete("Executed 0 of 0 ERROR (0.001 secs / 0 secs)\n").reason, "error");
  assert.equal(karmaIncomplete("Executed 0 of 12 SUCCESS (0 secs / 0 secs)\n").reason, "none");
  assert.equal(karmaIncomplete("Executed 10 of 12 SUCCESS (0.1 secs / 0.1 secs)\n").reason, "partial");
  assert.equal(karmaIncomplete("no karma here\n"), null);
});

test("exclusion patterns keep every name but the excluded ones, whatever characters they hold", () => {
  const names = ['Suite one hangs forever', 'quotes "and" / slashes (and) [brackets] $^.*+?'];
  const re = new RegExp(exclusionPattern(names));
  for (const n of names) assert.equal(re.test(n), false, n);
  assert.equal(re.test("Suite one passes"), true);
  assert.equal(re.test("Suite one hangs forever too"), true);
  assert.doesNotMatch(escapeJs(names[1]), /[ "/()[\]$^.*+?]/);
});

test("readProgress names the leaf tests that started and never ended", (t) => {
  const dir = outDir(t);
  const file = join(dir, "p.jsonl");
  const lines = [
    { event: "start", file: join(dir, "t/a.test.js"), name: "Suite one" },
    { event: "start", file: join(dir, "t/a.test.js"), name: "Suite one passes" },
    { event: "end", file: join(dir, "t/a.test.js"), name: "Suite one passes", status: "pass" },
    { event: "start", file: join(dir, "t/a.test.js"), name: "Suite one hangs forever" },
    { event: "start", name: "Karma spec" },
    { event: "end", name: "Karma spec", status: "fail", message: "boom" },
  ];
  writeFileSync(file, lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
  const p = readProgress(file, dir);
  assert.deepEqual(
    p.inflight.map((x) => x.id),
    ["t/a.test.js::Suite one hangs forever"],
  );
  assert.equal(p.ends.get("Karma spec").status, "fail");
});

test("baseLine maps head lines through insertions and deletions; pairFunctions follows it", () => {
  const diff = parseUnifiedDiff(["diff --git a/f b/f", "--- a/f", "+++ b/f", "@@ -2,0 +3,2 @@", "+x", "+y", "@@ -10,2 +11,0 @@", "-p", "-q"].join("\n")).get("f");
  assert.equal(baseLine(diff, 1), 1);
  assert.equal(baseLine(diff, 2), 2);
  assert.equal(baseLine(diff, 5), 3);
  assert.equal(baseLine(diff, 11), 9);
  assert.equal(baseLine(diff, 12), 12);
  const head = [{ name: "(anonymous)", longName: "(anonymous)( )", start: 14, ccn: 4 }];
  const base = [
    { name: "(anonymous)", longName: "(anonymous)( )", start: 2, ccn: 9 },
    { name: "(anonymous)", longName: "(anonymous)( )", start: 14, ccn: 3 },
  ];
  assert.equal(pairFunctions(head, base, diff).get(head[0]).ccn, 3);
});
