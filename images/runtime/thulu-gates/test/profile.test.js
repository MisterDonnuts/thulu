// The project's gate profile (THULU_GATES_PROFILE / --profile): settings Thulu keeps outside the
// client repo, merged over .thulu/gates.json, and accept_unverified, which turns a check a person
// accepted into "accepted" instead of an error.

import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { test } from "node:test";

import { CHECK_IDS } from "../lib/checks/index.js";
import { main } from "../lib/cli.js";
import { loadConfig } from "../lib/config.js";
import { setLocale } from "../lib/i18n.js";
import { acceptedChecks, loadProfile } from "../lib/profile.js";
import { applyAcceptance } from "../lib/report.js";
import { collect, fakeRunner, fakeWhich, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

const GO_BASE = {
  "go.mod": "module example.com/g1\n\ngo 1.22\n",
  "calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
};
const SUB = "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n";

function tmpFile(t, name, content) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-profile-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const p = join(dir, name);
  writeFileSync(p, typeof content === "string" ? content : JSON.stringify(content));
  return p;
}

async function gates(repo, t, { args = [], handlers = {}, tools = [], env = process.env }) {
  const out = outDir(t);
  const run = fakeRunner(handlers);
  const stderr = collect();
  const code = await main(["run", "--out", out, ...args], { run, which: fakeWhich(tools), fetch: noNetwork, cwd: repo.dir, stdout: collect(), stderr, env });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  const md = existsSync(join(out, "report.md")) ? readFileSync(join(out, "report.md"), "utf8") : "";
  return { code, report, md, check: (id) => report?.checks.find((c) => c.id === id), calls: run.calls, stderr: stderr.text() };
}

const goTimesOut = { go: () => ({ code: null, timedOut: true }) };

test("loadProfile reads a plain profile, or the entry named like the repo's directory", (t) => {
  const plain = tmpFile(t, "p.json", { schema: 1, coverage_min: 50 });
  assert.deepEqual(loadProfile({ flag: plain, root: "/w/projects/app" }).profile, { coverage_min: 50 });

  const many = tmpFile(t, "m.json", { schema: 1, projects: { app: { dup_max: 9 }, api: { dup_max: 1 } } });
  const app = loadProfile({ env: { THULU_GATES_PROFILE: many }, root: "/w/projects/app" });
  assert.deepEqual(app.profile, { dup_max: 9 });
  assert.equal(app.entry, "app");

  const none = loadProfile({ flag: many, root: "/w/projects/web" });
  assert.equal(none.profile, null);
  assert.deepEqual(none.warnings.map((w) => w.key), ["profile_no_entry"]);

  // A single entry is the run's only project, wherever the agent cloned it.
  const one = tmpFile(t, "o.json", { projects: { app: { dup_max: 2 } } });
  assert.deepEqual(loadProfile({ flag: one, root: "/tmp/elsewhere" }).profile, { dup_max: 2 });

  assert.deepEqual(loadProfile({ flag: "/no/such/profile.json", root: "/r" }).warnings.map((w) => w.key), ["profile_missing"]);
  assert.deepEqual(loadProfile({ flag: tmpFile(t, "bad.json", "[1,"), root: "/r" }).warnings.map((w) => w.key), ["profile_unparsable"]);
  assert.equal(loadProfile({ env: {}, root: "/r" }).profile, null);
});

test("the profile merges over the repo's gates.json and under the flags; only it may accept", () => {
  const repoFile = JSON.stringify({ coverage_min: 90, dup_max: 1, timeouts: { tests: 100 }, accept_unverified: { tests: "repo says so" } });
  const profile = { coverage_min: 60, timeouts: { test_stall: 0 }, commands: { tests: "npm run test:changed" }, accept_unverified: { tests: "Karma hangs on base" } };
  const { cfg, warnings } = loadConfig({ baseText: repoFile, headText: null, flags: { dupMax: 4 }, profile });
  assert.equal(cfg.coverage_min, 60);
  assert.equal(cfg.dup_max, 4);
  assert.equal(cfg.timeouts.tests, 100);
  assert.equal(cfg.timeouts.test_stall, 0);
  assert.equal(cfg.commands.tests, "npm run test:changed");
  assert.deepEqual(cfg.accept_unverified, { tests: "Karma hangs on base" });
  assert.deepEqual(warnings, ["config_accept_ignored"]);

  const repoOnly = loadConfig({ baseText: repoFile, headText: null, flags: {} });
  assert.equal(repoOnly.cfg.accept_unverified, undefined);
});

test("acceptedChecks keeps known checks with a reason and drops the rest with a warning", () => {
  const r = acceptedChecks({ tests: " Karma disconnects on base ", nope: "x", lint: "  " }, CHECK_IDS);
  assert.deepEqual(r.accepted, { tests: "Karma disconnects on base" });
  assert.deepEqual(r.warnings.map((w) => w.key).sort(), ["accept_no_reason", "accept_unknown_check"]);
  assert.deepEqual(acceptedChecks("tests", CHECK_IDS).warnings.map((w) => w.key), ["accept_not_object"]);
});

test("applyAcceptance never accepts a check with blocking findings or one that finished", () => {
  const checks = [
    { id: "tests", status: "error", blocking: false, metrics: { not_verified: true }, summary: "timed out" },
    { id: "coverage_new", status: "skip", blocking: false, metrics: { not_verified: true }, summary: "tests did not finish" },
    { id: "lint", status: "error", blocking: true, metrics: {}, summary: "crashed after a finding" },
    { id: "duplication", status: "pass", blocking: false, metrics: {}, summary: "ok" },
  ];
  const out = applyAcceptance(checks, { tests: "a", coverage_new: "b", lint: "c", duplication: "d" });
  assert.deepEqual(
    out.map((c) => c.status),
    ["accepted", "accepted", "error", "pass"],
  );
  assert.deepEqual(out[0].metrics.accepted, { reason: "a", status: "error" });
  assert.equal(checks[0].status, "error", "the input is not changed");
});

test("a check the profile accepts reports 'accepted' with the reason, and the verdict passes", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const profile = tmpFile(t, "profile.json", {
    schema: 1,
    projects: { [basename(repo.dir)]: { accept_unverified: { tests: "Karma disconnects on base; accepted on 2026-11-24", coverage_new: "depends on tests" } } },
  });
  const r = await gates(repo, t, { args: ["--only", "tests,coverage_new", "--profile", profile], handlers: goTimesOut, tools: ["go"] });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.report.verdict, "pass");
  assert.deepEqual(r.report.errors, []);
  assert.deepEqual(r.report.blocking, []);
  assert.deepEqual(r.report.not_verified, []);
  assert.equal(r.check("tests").status, "accepted");
  assert.equal(r.check("tests").blocking, false);
  assert.equal(r.check("coverage_new").status, "accepted");
  assert.deepEqual(
    r.report.accepted.map((a) => [a.id, a.status, a.reason]),
    [
      ["tests", "error", "Karma disconnects on base; accepted on 2026-11-24"],
      ["coverage_new", "skip", "depends on tests"],
    ],
  );
  assert.equal(r.report.config.profile, profile);
  assert.match(r.md, /## Accepted as not verified \(project profile\)/);
  assert.match(r.md, /- tests: accepted: Karma disconnects on base; accepted on 2026-11-24\. Why it was not verified: /);
  assert.match(r.md, /\| tests \| accepted \|/);
});

test("without a reason, or from the repo's own gates.json, nothing is accepted", async (t) => {
  const repo = makeRepo({ ...GO_BASE, ".thulu/gates.json": JSON.stringify({ accept_unverified: { tests: "the repo cannot decide this" } }) }, t);
  repo.commit({ "calc/calc.go": SUB });
  const profile = tmpFile(t, "profile.json", { accept_unverified: { tests: "" } });
  const r = await gates(repo, t, { args: ["--only", "tests", "--profile", profile], handlers: goTimesOut, tools: ["go"] });
  assert.equal(r.code, 2);
  assert.equal(r.check("tests").status, "error");
  assert.deepEqual(r.report.accepted, []);
  assert.match(r.stderr, /gives tests no reason/);
  assert.match(r.stderr, /only the project profile in Thulu can set/);
});

test("the profile's commands and timeouts reach the checks; THULU_GATES_PROFILE names it", async (t) => {
  const repo = makeRepo(GO_BASE, t);
  repo.commit({ "calc/calc.go": SUB });
  const profile = tmpFile(t, "profile.json", { commands: { tests: "make test-changed" }, timeouts: { tests: 42 } });
  let timeoutMs;
  const r = await gates(repo, t, {
    args: ["--only", "tests"],
    handlers: {
      sh: (args, opts) => {
        timeoutMs = opts.timeoutMs;
        return { code: 0 };
      },
    },
    env: { ...process.env, THULU_GATES_PROFILE: profile },
  });
  const sh = r.calls.find((c) => c.cmd === "sh");
  assert.deepEqual(sh.args, ["-c", "make test-changed"]);
  assert.equal(timeoutMs, 42_000);
});
