// Thulu commits whatever a run leaves in the repo, so the tools the gates run must not leave their
// caches, sandboxes or reports there.

import assert from "node:assert/strict";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { main } from "../lib/cli.js";
import { setLocale } from "../lib/i18n.js";
import { collect, fakeRunner, fakeWhich, git, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

function put(path, text) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
}

async function runGates(repo, t, { args, handlers, tools }) {
  const out = outDir(t);
  const run = fakeRunner(handlers);
  const code = await main(["run", "--quiet", "--out", out, ...args], {
    run,
    which: fakeWhich(tools),
    fetch: noNetwork,
    cwd: repo.dir,
    stdout: collect(),
    stderr: collect(),
  });
  const report = JSON.parse(readFileSync(join(out, "report.json"), "utf8"));
  return { code, out, calls: run.calls, check: (id) => report.checks.find((c) => c.id === id) };
}

const TS_BASE = {
  ".gitignore": "node_modules/\n",
  "package.json": JSON.stringify({ name: "s", devDependencies: { "@stryker-mutator/core": "8.0.0" } }),
  "tsconfig.json": "{}\n",
  "src/price.ts": "export const price = (n: number) => n;\n",
  "reports/README.md": "the project's own reports\n",
};

test("Stryker leaves no sandbox, report or incremental file in the repo; its report goes to --out", async (t) => {
  const repo = makeRepo(TS_BASE, t);
  repo.commit({ "src/price.ts": "export const price = (n: number) => (n > 100 ? n * 0.9 : n);\n" });
  put(join(repo.dir, "node_modules/.bin/stryker"), "#!/bin/sh\n");
  const mutation = {
    files: { "src/price.ts": { mutants: [{ mutatorName: "ConditionalExpression", status: "Survived", location: { start: { line: 1 } } }] } },
  };
  let incremental = null;
  const stryker = (args, opts) => {
    if (args[0] !== "run") return { stdout: "8.0.0" };
    incremental = args[args.indexOf("--incrementalFile") + 1];
    put(join(opts.cwd, ".stryker-tmp/sandbox-1/src/price.ts"), "mutated\n");
    put(join(opts.cwd, "reports/mutation/mutation.json"), JSON.stringify(mutation));
    put(incremental, "{}");
    return { code: 0 };
  };

  const r = await runGates(repo, t, { args: ["--only", "mutation"], handlers: { stryker } });
  assert.equal(git(repo.dir, "status", "--porcelain", "--ignored=no"), "");
  assert.equal(readFileSync(join(repo.dir, "reports/README.md"), "utf8"), "the project's own reports\n");
  assert.ok(incremental.startsWith(r.out), `incremental file ${incremental} is outside --out`);
  assert.deepEqual(JSON.parse(readFileSync(join(r.out, "raw", "stryker-root.json"), "utf8")), mutation);
  assert.equal(r.check("mutation").metrics.survived, 1);
});

test("a mutation report the project already tracks is written back after Stryker overwrites it", async (t) => {
  const repo = makeRepo({ ...TS_BASE, "reports/mutation/mutation.json": '{"files":{}}\n' }, t);
  repo.commit({ "src/price.ts": "export const price = (n: number) => n + 1;\n" });
  put(join(repo.dir, "node_modules/.bin/stryker"), "#!/bin/sh\n");
  const stryker = (args, opts) => {
    if (args[0] === "run") put(join(opts.cwd, "reports/mutation/mutation.json"), '{"files":{"src/price.ts":{"mutants":[]}}}');
    return { code: 0 };
  };

  await runGates(repo, t, { args: ["--only", "mutation"], handlers: { stryker } });
  assert.equal(readFileSync(join(repo.dir, "reports/mutation/mutation.json"), "utf8"), '{"files":{}}\n');
  assert.equal(git(repo.dir, "status", "--porcelain"), "");
});

test("mutmut's cache does not stay in the repo", async (t) => {
  const repo = makeRepo({ "pyproject.toml": "[project]\nname = \"p\"\n", "p/calc.py": "def add(a, b):\n    return a + b\n" }, t);
  repo.commit({ "p/calc.py": "def add(a, b):\n    return a + b + 0\n" });
  const mutmut = (args, opts) => {
    if (args[0] === "--version") return { stdout: "mutmut version 2.4.4" };
    if (args[0] === "run") put(join(opts.cwd, ".mutmut-cache"), "sqlite");
    return { code: 0, stdout: "" };
  };

  await runGates(repo, t, { args: ["--only", "mutation"], handlers: { mutmut }, tools: ["mutmut"] });
  assert.equal(existsSync(join(repo.dir, ".mutmut-cache")), false);
  assert.equal(git(repo.dir, "status", "--porcelain"), "");
});

test("pytest keeps coverage data in --out and writes no bytecode next to the code", async (t) => {
  const repo = makeRepo({ "pyproject.toml": "[project]\nname = \"p\"\n", "p/calc.py": "def add(a, b):\n    return a + b\n" }, t);
  repo.commit({ "p/calc.py": "def add(a, b):\n    return a + b + 0\n", "tests/test_calc.py": "def test_add():\n    assert True\n" });
  const pytest = (args, opts) => {
    if (args.includes("--version")) return { stdout: "pytest 8.3.0" };
    // What pytest-cov does with COVERAGE_FILE unset: .coverage in the cwd.
    put(opts.env?.COVERAGE_FILE || join(opts.cwd, ".coverage"), "data");
    return { code: 0 };
  };

  const r = await runGates(repo, t, { args: ["--only", "tests"], handlers: { pytest, python3: () => ({ code: 0 }) }, tools: ["pytest", "python3"] });
  const call = r.calls.find((c) => c.cmd === "pytest" && c.args.some((a) => a.startsWith("--junitxml=")));
  assert.ok(call.env.COVERAGE_FILE.startsWith(r.out), `COVERAGE_FILE ${call.env.COVERAGE_FILE} is outside --out`);
  assert.equal(call.env.PYTHONDONTWRITEBYTECODE, "1");
  assert.equal(git(repo.dir, "status", "--porcelain"), "");
});

test("a tracked report comes back even when the tool removed its directory", async (t) => {
  const repo = makeRepo({ ...TS_BASE, "reports/mutation/mutation.json": '{"files":{}}\n' }, t);
  repo.commit({ "src/price.ts": "export const price = (n: number) => n + 2;\n" });
  put(join(repo.dir, "node_modules/.bin/stryker"), "#!/bin/sh\n");
  const stryker = (args, opts) => {
    if (args[0] === "run") rmSync(join(opts.cwd, "reports/mutation"), { recursive: true, force: true });
    return { code: 0 };
  };

  const r = await runGates(repo, t, { args: ["--only", "mutation"], handlers: { stryker } });
  assert.equal(readFileSync(join(repo.dir, "reports/mutation/mutation.json"), "utf8"), '{"files":{}}\n');
  assert.equal(git(repo.dir, "status", "--porcelain"), "");
  assert.notEqual(r.check("mutation").status, "error");
});
