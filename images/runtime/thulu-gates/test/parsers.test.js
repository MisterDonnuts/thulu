import assert from "node:assert/strict";
import { test } from "node:test";

import { parseGovulncheck, parseNpmAudit, parseOsv } from "../lib/checks/deps.js";
import { detectClones, tokenize } from "../lib/checks/duplication.js";
import { parseLizardCsv } from "../lib/checks/lint.js";
import { parseGremlins, parsePit, parseStryker } from "../lib/checks/mutation.js";
import { compare, parseGoBench } from "../lib/checks/perf.js";
import { gateConfigReason, inspect } from "../lib/checks/suppressions.js";
import { parseMypy, parseTsc } from "../lib/checks/typecheck.js";
import { exactVersion, parseCargo, parseGoMod, parseNpm, parsePom, parsePyproject, parseRequirements } from "../lib/deps.js";
import { parseUnifiedDiff } from "../lib/git.js";
import { parseCobertura, parseGoCover, parseJacoco, parseLcov } from "../lib/parsers.js";
import { fromSarif, toSarif, withFingerprints } from "../lib/sarif.js";
import { mannWhitney, median, medianCI } from "../lib/stats.js";
import { versionOf } from "../lib/tools.js";
import { globToRegExp, isTestFile, matchesAny, ranges } from "../lib/util.js";

test("parseUnifiedDiff: added lines, renames and a removed line that looks like a header", () => {
  const text = [
    "diff --git a/a.go b/a.go",
    "index 1..2 100644",
    "--- a/a.go",
    "+++ b/a.go",
    "@@ -3,0 +4,2 @@",
    "+x := 1",
    "+y := 2",
    "@@ -10 +11,0 @@",
    "--- a SQL comment that was deleted",
    "diff --git a/old.py b/new.py",
    "similarity index 90%",
    "rename from old.py",
    "rename to new.py",
    "--- a/old.py",
    "+++ b/new.py",
    "@@ -1 +1 @@",
    "-a = 1",
    "+a = 2",
    "diff --git a/n.ts b/n.ts",
    "new file mode 100644",
    "--- /dev/null",
    "+++ b/n.ts",
    "@@ -0,0 +1,1 @@",
    "+export const n = 1;",
  ].join("\n");
  const d = parseUnifiedDiff(text);
  assert.deepEqual([...d.get("a.go").added], [4, 5]);
  assert.equal(d.get("a.go").deletedCount, 1);
  assert.deepEqual(d.get("a.go").removed, ["-- a SQL comment that was deleted"]);
  assert.equal(d.get("new.py").oldPath, "old.py");
  assert.deepEqual([...d.get("new.py").added], [1]);
  assert.equal(d.get("n.ts").status, "A");
});

test("globs: ** across dirs, basename-only patterns, braces", () => {
  assert.ok(globToRegExp("src/**/*.ts").test("src/a/b/c.ts"));
  assert.ok(globToRegExp("src/**/*.ts").test("src/c.ts"));
  assert.ok(matchesAny("deep/dir/payments.go", ["payments.go"]));
  assert.ok(matchesAny("x/vite.config.mjs", ["{vitest,jest,vite}.config.*"]));
  assert.ok(!matchesAny("src/a.ts", ["lib/**"]));
  assert.ok(isTestFile("pkg/a_test.go"));
  assert.ok(isTestFile("web/src/__tests__/x.ts"));
  assert.ok(isTestFile("tests/test_api.py"));
  assert.ok(!isTestFile("pkg/a.go"));
  assert.deepEqual(ranges([1, 2, 3, 7, 9, 10]), [[1, 3], [7, 7], [9, 10]]);
});

test("coverage parsers: lcov, go cover, cobertura, jacoco", () => {
  const lcov = parseLcov("SF:src/a.ts\nDA:1,3\nDA:2,0\nend_of_record\n");
  assert.equal(lcov.get("src/a.ts").get(1), 3);
  assert.equal(lcov.get("src/a.ts").get(2), 0);

  const go = parseGoCover("mode: atomic\nexample.com/m/calc/c.go:5.24,7.2 1 0\nexample.com/m/calc/c.go:3.30,3.40 1 4\n");
  assert.equal(go.get("example.com/m/calc/c.go").get(6), 0);
  assert.equal(go.get("example.com/m/calc/c.go").get(3), 4);

  const cob = parseCobertura(
    '<coverage><sources><source>/repo/src</source></sources><packages><package><classes><class filename="pkg/m.py"><lines><line number="4" hits="0"/><line number="5" hits="2"/></lines></class></classes></package></packages></coverage>',
  );
  assert.deepEqual(cob.sources, ["/repo/src"]);
  assert.equal(cob.map.get("pkg/m.py").get(5), 2);

  const jac = parseJacoco('<report><package name="com/x"><sourcefile name="A.java"><line nr="10" mi="0" ci="3"/><line nr="11" mi="2" ci="0"/></sourcefile></package></report>');
  assert.equal(jac.get("com/x/A.java").get(10) > 0, true);
  assert.equal(jac.get("com/x/A.java").get(11), 0);
});

test("duplication: a copied block in another file is a clone; distinct code is not", () => {
  const body = Array.from({ length: 12 }, (_, i) => `  total = total + values[${i}] * weight(${i}, "k${i}");`).join("\n");
  const a = `function a(values) {\n  let total = 0;\n${body}\n  return total;\n}\n`;
  const b = `function b(values) {\n  let total = 0;\n${body}\n  return total;\n}\n`;
  const added = new Set(a.split("\n").map((_, i) => i + 1));
  const res = detectClones([{ file: "src/a.js", text: a, added }], [{ file: "src/a.js", text: a }, { file: "src/b.js", text: b }], 50);
  const matched = res.get("src/a.js").matched.filter(Boolean);
  assert.ok(matched.length > 0);
  assert.equal(matched[0].file, "src/b.js");

  const other = "export const sum = (xs) => xs.reduce((a, b) => a + b, 0);\n";
  const res2 = detectClones([{ file: "src/a.js", text: a, added }], [{ file: "src/c.js", text: other }], 50);
  assert.equal(res2.get("src/a.js").matched.filter(Boolean).length, 0);
  assert.ok(tokenize("x := 1 // note", "go").every((t) => t.v !== "note"));
});

test("suppressions: rule and reason are required; markers in strings of this file do not match", () => {
  assert.equal(inspect("x() //" + "nolint").problem, "no_rule");
  assert.equal(inspect("x() //" + "nolint:errcheck").problem, "no_reason");
  assert.equal(inspect("x() //" + "nolint:errcheck // close error is logged by the caller already").ok, true);
  assert.equal(inspect("/* eslint-" + "disable */").problem, "file_level");
  assert.equal(inspect("// eslint-" + "disable-next-line no-console -- startup banner goes to stdout on purpose").ok, true);
  assert.equal(inspect("import x  # no" + "qa").problem, "no_rule");
  assert.equal(inspect("y = f()  # type: " + "ignore[attr-defined]  stubs for this library lag behind upstream").ok, true);
  assert.equal(inspect("const ok = 1;"), null);
});

test("gate config: CI and linter configs always count; package.json only when gate lines change", () => {
  const d = (added = [], removed = []) => ({ added: new Set(added.map((_, i) => i + 1)), removed, _text: (n) => added[n - 1] });
  assert.equal(gateConfigReason(".golangci.yml", d()), "always");
  assert.equal(gateConfigReason(".github/workflows/ci.yml", d()), "always");
  assert.equal(gateConfigReason("package.json", d(['    "left-pad": "1.3.0"'])), null);
  assert.equal(gateConfigReason("package.json", d(['    "lint": "true"'])), "lines");
});

test("deps parsers", () => {
  const npm = parseNpm(JSON.stringify({ dependencies: { a: "^1.0.0", local: "file:../x" }, devDependencies: { b: "2.0.0" } }));
  assert.deepEqual([...npm.keys()], ["a", "b"]);
  assert.equal(npm.get("b").dev, true);

  const gm = parseGoMod("module m\n\nrequire (\n\tgithub.com/x/y v1.2.3\n\tgithub.com/z/w v0.1.0 // indirect\n\tgithub.com/local/l v0.0.0\n)\n\nreplace github.com/local/l => ../l\n");
  assert.deepEqual([...gm.keys()], ["github.com/x/y", "github.com/z/w"]);
  assert.equal(gm.get("github.com/z/w").indirect, true);

  assert.deepEqual([...parseRequirements("requests==2.31.0\n# c\n-r other.txt\nflask>=2\n").keys()], ["requests", "flask"]);
  const py = parsePyproject('[project]\nname = "x"\ndependencies = [\n  "httpx>=0.27",\n  "uvicorn[standard]==0.30.0",\n]\n');
  assert.deepEqual([...py.keys()], ["httpx", "uvicorn"]);
  assert.ok(parseCargo('[dependencies]\nserde = "1.0"\ntokio = { version = "1", features = ["full"] }\n').has("tokio"));
  assert.ok(parsePom("<dependencies><dependency><groupId>org.x</groupId><artifactId>y</artifactId><version>1.0</version></dependency></dependencies>").has("org.x:y"));
  assert.equal(exactVersion("npm", "^1.0.0"), null);
  assert.equal(exactVersion("npm", "1.2.3"), "1.2.3");
  assert.equal(exactVersion("pypi", "==2.31.0"), "2.31.0");
});

test("vulnerability parsers: osv, npm audit, govulncheck (only reachable symbols)", () => {
  const osv = parseOsv({
    results: [
      {
        source: { path: "/r/go.mod" },
        packages: [
          {
            package: { name: "github.com/x/y", version: "1.0.0", ecosystem: "Go" },
            vulnerabilities: [{ id: "GO-1", summary: "bad", affected: [{ ranges: [{ events: [{ introduced: "0" }, { fixed: "1.0.1" }] }] }], database_specific: { severity: "HIGH" } }],
            groups: [{ ids: ["GO-1"], max_severity: "7.5" }],
          },
        ],
      },
    ],
  });
  assert.equal(osv.length, 1);
  assert.equal(osv[0].package, "github.com/x/y");
  assert.equal(osv[0].fixed, true);
  assert.equal(osv[0].severity, "high");

  const audit = parseNpmAudit({ vulnerabilities: { lodash: { name: "lodash", severity: "critical", fixAvailable: true, range: "<4.17.21", via: [{ title: "Prototype pollution", url: "https://x/GHSA-1" }] } } });
  assert.equal(audit[0].severity, "critical");
  assert.equal(audit[0].fixed, true);

  const gv = [
    JSON.stringify({ osv: { id: "GO-2", summary: "s", affected: [{ package: { name: "golang.org/x/net" }, ranges: [{ events: [{ fixed: "0.30.0" }] }] }] } }),
    JSON.stringify({ finding: { osv: "GO-2", fixed_version: "v0.30.0", trace: [{ module: "golang.org/x/net", version: "v0.20.0" }] } }),
    JSON.stringify({ osv: { id: "GO-3", summary: "called", affected: [{ package: { name: "golang.org/x/text" } }] } }),
    JSON.stringify({ finding: { osv: "GO-3", fixed_version: "v0.14.0", trace: [{ module: "golang.org/x/text", version: "v0.13.0", function: "Parse" }, { module: "m", function: "main", position: { filename: "cmd/main.go", line: 9 } }] } }),
  ].join("\n");
  const found = parseGovulncheck(gv);
  assert.deepEqual(found.map((v) => v.id), ["GO-3"]);
  assert.equal(found[0].callers[0].file, "cmd/main.go");
});

test("stats: median, CI and Mann-Whitney separate shifted samples and not equal ones", () => {
  assert.equal(median([3, 1, 2]), 2);
  assert.equal(median([4, 1, 2, 3]), 2.5);
  const [lo, hi] = medianCI([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
  assert.ok(lo <= 5.5 && hi >= 5.5);
  const a = [100, 101, 99, 100, 102, 98, 100, 101, 99, 100];
  const b = a.map((x) => x * 1.25);
  assert.ok(mannWhitney(a, b).p < 0.01);
  assert.ok(mannWhitney(a, [...a].reverse()).p > 0.5);
});

test("perf: go bench parsing and verdicts", () => {
  const samples = parseGoBench("goos: linux\npkg: m/calc\nBenchmarkAdd-8   \t1000000\t       100.0 ns/op\nBenchmarkAdd-8   \t1000000\t       102.0 ns/op\nPASS\n");
  assert.deepEqual(samples.get("m/calc BenchmarkAdd"), [100, 102]);

  const base = new Map([["A", [100, 101, 99, 100, 102, 98, 100, 101, 99, 100]], ["B", [50, 51, 49, 50, 50, 51, 49, 50, 50, 50]]]);
  const head = new Map([
    ["A", base.get("A").map((x) => x * 1.3)],
    ["B", [50, 49, 51, 50, 50, 50, 51, 49, 50, 50]],
    ["C", [1, 2, 3]],
  ]);
  const rows = Object.fromEntries(compare(base, head, { alpha: 0.05, thresholdPct: 10 }).map((r) => [r.name, r.verdict]));
  assert.deepEqual(rows, { A: "regression", B: "no_change", C: "new" });
});

test("mutation report parsers", () => {
  const g = parseGremlins({ files: [{ file_name: "a.go", mutations: [{ line: 3, status: "LIVED", type: "CONDITIONALS_NEGATION" }, { line: 4, status: "KILLED", type: "x" }] }] });
  assert.equal(g.filter((m) => m.status === "survived").length, 1);
  const s = parseStryker({ files: { "src/a.ts": { mutants: [{ status: "Survived", mutatorName: "EqualityOperator", location: { start: { line: 2 } } }, { status: "Killed", mutatorName: "x", location: { start: { line: 3 } } }, { status: "NoCoverage", mutatorName: "y", location: { start: { line: 4 } } }] } } });
  assert.deepEqual(s.map((m) => m.status), ["survived", "killed", "no_coverage"]);
  const p = parsePit('<mutations><mutation detected="false" status="SURVIVED"><sourceFile>A.java</sourceFile><mutatedClass>com.x.A</mutatedClass><lineNumber>7</lineNumber><mutator>NegateConditionals</mutator></mutation></mutations>');
  assert.equal(p[0].status, "survived");
  assert.equal(p[0].line, 7);
});

test("typecheck and lint output parsers", () => {
  const tsc = parseTsc("src/a.ts(3,5): error TS2322: Type 'string' is not assignable to type 'number'.\n", "");
  assert.equal(tsc[0].file, "src/a.ts");
  assert.equal(tsc[0].line, 3);
  assert.equal(tsc[0].code, "TS2322");
  const my = parseMypy('pkg/m.py:4: error: Incompatible return value type  [return-value]\nFound 1 error in 1 file\n', "");
  assert.equal(my[0].code, "return-value");
  const lz = parseLizardCsv('15,8,120,2,20,"handle@10-30@src/a.ts","src/a.ts","handle","handle( req )",10,30\n');
  assert.equal(lz[0].ccn, 8);
  assert.equal(lz[0].file, "src/a.ts");
});

test("sarif round trip keeps rule, severity, location and a stable fingerprint", () => {
  const findings = [{ rule: "r1", severity: "high", file: "a.go", line: 2, message: "m" }];
  withFingerprints("tool", "/nonexistent", findings);
  const doc = toSarif([{ tool: "tool", version: "1.0", findings }]);
  assert.equal(doc.version, "2.1.0");
  const back = fromSarif(doc, "/r");
  assert.equal(back[0].rule, "r1");
  assert.equal(back[0].severity, "high");
  assert.equal(back[0].file, "a.go");
  assert.equal(back[0].line, 2);
  assert.ok(doc.runs[0].results[0].partialFingerprints["thuluGates/v1"]);
});

test("versionOf pulls the number out of a banner", () => {
  assert.equal(versionOf("golangci-lint has version 2.12.2 built with go1.26.2"), "2.12.2");
  assert.equal(versionOf("go version go1.26.3 darwin/arm64"), "1.26.3");
  assert.equal(versionOf("v22.11.0"), "22.11.0");
  assert.equal(versionOf("semgrep"), "semgrep");
});
