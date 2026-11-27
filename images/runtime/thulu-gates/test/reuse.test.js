// --reuse: a clean tree's results are stored in the cache under (head, base, version, effective
// config, quarantine), and a later run with --reuse on the same key hands them back without
// running the checks; with --only it runs the listed checks again and merges.

import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { main } from "../lib/cli.js";
import { setLocale } from "../lib/i18n.js";
import { configHash, plan } from "../lib/reuse.js";
import { collect, fakeRunner, fakeWhich, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const GO_BASE = {
  "go.mod": "module example.com/g1\n\ngo 1.22\n",
  "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
};
const SUB = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n";

// go fakes `go test` with full coverage, counting its calls.
function goFake() {
  const fn = (args) => {
    if (args[0] === "test") {
      fn.tests++;
      const prof = args.find((a) => a.startsWith("-coverprofile="))?.slice("-coverprofile=".length);
      if (prof) writeFileSync(prof, "mode: atomic\nexample.com/g1/calc/calc.go:1.1,40.2 1 1\n");
      return { stdout: JSON.stringify({ Action: "pass", Package: "example.com/g1/calc" }) + "\n" };
    }
    if (args[0] === "vet") fn.vets++;
    return { code: 0 };
  };
  fn.tests = 0;
  fn.vets = 0;
  return fn;
}

async function gates(repo, t, { args = [], go = goFake(), env = process.env } = {}) {
  const out = outDir(t);
  const stdout = collect();
  const stderr = collect();
  const code = await main(["run", "--out", out, ...args], { run: fakeRunner({ go }), which: fakeWhich(["go"]), fetch: noNetwork, cwd: repo.dir, stdout, stderr, env });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  return { code, out, report, md: existsSync(join(out, "report.md")) ? readFileSync(join(out, "report.md"), "utf8") : "", check: (id) => report?.checks.find((c) => c.id === id), go, stderr: stderr.text() };
}

function cacheDir(t) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-reuse-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test("--reuse on the head a clean run measured returns the stored report without running a check", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const cache = cacheDir(t);
  const first = await gates(repo, t, { args: ["--cache-dir", cache] });
  assert.equal(first.code, 0, first.stderr);
  assert.equal(first.go.tests, 1);
  assert.equal(first.report.reused, null);

  const second = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse"] });
  assert.equal(second.code, 0, second.stderr);
  assert.equal(second.go.tests, 0, "tests did not run again");
  assert.equal(second.go.vets, 0, "lint did not run again");
  assert.equal(second.report.verdict, first.report.verdict);
  assert.deepEqual(
    second.report.checks.map((c) => [c.id, c.status, c.summary]),
    first.report.checks.map((c) => [c.id, c.status, c.summary]),
  );
  assert.equal(second.report.reused.from, first.report.started_at);
  assert.equal(second.report.reused.out, first.out);
  assert.deepEqual(second.report.reused.rerun, []);
  assert.match(second.md, new RegExp(`Reused from ${first.report.started_at.replace(/[.]/g, "\\.")} \\(the run in \`${first.out}\`\\): no check ran again\\.`));
  // The SARIF each reused check had comes back in the new --out.
  for (const c of second.report.checks.filter((x) => x.sarif)) assert.ok(existsSync(join(second.out, c.sarif)), c.sarif);
});

test("THULU_GATES_REUSE=1 reuses like the flag; without either, a run measures again", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const cache = cacheDir(t);
  await gates(repo, t, { args: ["--cache-dir", cache] });
  const plain = await gates(repo, t, { args: ["--cache-dir", cache] });
  assert.equal(plain.go.tests, 1);
  assert.equal(plain.report.reused, null);
  const viaEnv = await gates(repo, t, { args: ["--cache-dir", cache], env: { ...process.env, THULU_GATES_REUSE: "1" } });
  assert.equal(viaEnv.go.tests, 0);
  assert.ok(viaEnv.report.reused);
});

test("a dirty tree, a new commit or a different config never reuses", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const cache = cacheDir(t);
  await gates(repo, t, { args: ["--cache-dir", cache] });

  const otherConfig = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse", "--coverage-min", "10"] });
  assert.equal(otherConfig.go.tests, 1);
  assert.equal(otherConfig.report.reused, null);
  assert.match(otherConfig.stderr, /no stored report for this head, base and config/);

  repo.write({ "calc/extra.go": "package calc\n" });
  const dirty = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse"] });
  assert.equal(dirty.go.tests, 1);
  assert.equal(dirty.report.reused, null);
  assert.match(dirty.stderr, /uncommitted or untracked files/);

  repo.commit({ "calc/extra.go": "package calc\n" });
  const moved = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse"] });
  assert.equal(moved.go.tests, 1);
  assert.equal(moved.report.reused, null);
});

test("--reuse with --only runs the listed checks again and keeps the rest from the store", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const cache = cacheDir(t);
  const first = await gates(repo, t, { args: ["--cache-dir", cache, "--only", "tests,coverage_new,lint"] });
  const merged = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse", "--only", "lint"] });
  assert.equal(merged.code, 0, merged.stderr);
  assert.equal(merged.go.tests, 0, "tests came from the store");
  assert.ok(merged.go.vets > 0, "lint ran again");
  assert.equal(merged.check("tests").status, first.check("tests").status);
  assert.equal(merged.check("coverage_new").status, first.check("coverage_new").status);
  assert.deepEqual(merged.report.reused.checks, ["tests", "coverage_new"]);
  assert.deepEqual(merged.report.reused.rerun, ["lint"]);
  assert.match(merged.md, /ran again: lint\./);
  // Checks the first run never measured stay out, as --only leaves them.
  assert.equal(merged.check("sast").status, "skip");
});

test("a check that ended in error runs again on reuse, unless the profile accepts it", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const cache = cacheDir(t);
  let calls = 0;
  const hang = (args) => {
    if (args[0] !== "test") return { code: 0 };
    calls++;
    return { code: null, timedOut: true };
  };
  const first = await gates(repo, t, { args: ["--cache-dir", cache], go: hang });
  assert.equal(first.check("tests").status, "error");
  await gates(repo, t, { args: ["--cache-dir", cache, "--reuse"], go: hang });
  assert.equal(calls, 2, "an errored check is measured again");

  const dir = cacheDir(t);
  const profile = join(dir, "profile.json");
  writeFileSync(profile, JSON.stringify({ accept_unverified: { tests: "Karma hangs on base" } }));
  // Accepting changes no check result, so the key is the same and the stored error is reused.
  const accepted = await gates(repo, t, { args: ["--cache-dir", cache, "--reuse", "--profile", profile], go: hang });
  assert.equal(calls, 2, "accepted: not run again");
  assert.equal(accepted.check("tests").status, "accepted");
  assert.ok(accepted.report.reused.checks.includes("tests"));
  assert.ok(!accepted.report.errors.includes("tests"));
});

test("plan: coverage_new running brings tests with it; configHash ignores the profile path and acceptance", () => {
  const entry = { ran: ["tests", "coverage_new"], checks: [{ id: "tests", status: "pass" }, { id: "coverage_new", status: "pass" }] };
  const { steps } = plan({ ids: ["tests", "coverage_new", "lint"], wanted: () => true, only: ["coverage_new"], entry });
  assert.deepEqual([...steps], [
    ["tests", "run"],
    ["coverage_new", "run"],
    ["lint", "leave"],
  ]);
  const noOnly = plan({ ids: ["tests", "coverage_new", "lint"], wanted: () => true, only: null, entry }).steps;
  assert.deepEqual([...noOnly], [
    ["tests", "reuse"],
    ["coverage_new", "reuse"],
    ["lint", "run"],
  ]);
  const a = { coverage_min: 80, profile: "/runs/1/p.json", accept_unverified: { tests: "x" }, source: "base" };
  const b = { coverage_min: 80, profile: "/runs/2/p.json", source: "head" };
  assert.equal(configHash(a), configHash(b));
  assert.notEqual(configHash(a), configHash({ ...b, coverage_min: 70 }));
});
