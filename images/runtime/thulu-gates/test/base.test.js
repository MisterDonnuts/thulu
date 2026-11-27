// Where the base comes from in the sandboxes Thulu runs: a pod's clone holds only the task branch,
// and Docker mounts the project's .git read-only.

import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { basename, join } from "node:path";
import { test } from "node:test";

import { main } from "../lib/cli.js";
import { run as realRun } from "../lib/exec.js";
import { addWorktree, removeWorktree } from "../lib/git.js";
import { setLocale } from "../lib/i18n.js";
import { collect, fakeRunner, fakeWhich, git, makeRepo, noNetwork, outDir } from "./helpers.js";

setLocale("en");

async function runGates(repo, t, args) {
  const out = outDir(t);
  const stderr = collect();
  const code = await main(["run", "--quiet", "--out", out, ...args], {
    run: fakeRunner(),
    which: fakeWhich([]),
    fetch: noNetwork,
    cwd: repo.dir,
    stdout: collect(),
    stderr,
  });
  const report = existsSync(join(out, "report.json")) ? JSON.parse(readFileSync(join(out, "report.json"), "utf8")) : null;
  return { code, report, stderr: stderr.text() };
}

test("a clone without the default branch asks for --base, and runs with the commit it names", async (t) => {
  const repo = makeRepo({ "a.txt": "base\n" }, t);
  repo.commit({ "a.txt": "head\n" });
  const base = git(repo.dir, "rev-parse", "main").trim();
  git(repo.dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD");
  git(repo.dir, "update-ref", "-d", "refs/remotes/origin/main");
  git(repo.dir, "branch", "-D", "main");

  const bare = await runGates(repo, t, ["--only", "suppressions"]);
  assert.equal(bare.code, 2);
  assert.match(bare.stderr, /origin\/main/);
  assert.match(bare.stderr, /--base/);

  const withBase = await runGates(repo, t, ["--base", base, "--only", "suppressions"]);
  assert.equal(withBase.code, 0);
  assert.equal(withBase.report.base, base);
});

test("a .git that cannot register a worktree gets base exported instead, and cleaned up", async (t) => {
  const repo = makeRepo({ "a.txt": "base\n", "dir/b.txt": "kept\n" }, t);
  repo.commit({ "a.txt": "head\n" });
  const base = git(repo.dir, "rev-parse", "main").trim();
  const readOnly = async (cmd, args = [], opts = {}) => {
    if (basename(cmd) === "git" && args[0] === "worktree" && args[1] === "add") {
      return { code: 128, stdout: "", stderr: "fatal: could not create leading directories of '.git/worktrees': Read-only file system" };
    }
    return realRun(cmd, args, opts);
  };

  const dir = await addWorktree(readOnly, repo.dir, base);
  assert.equal(readFileSync(join(dir, "a.txt"), "utf8"), "base\n");
  assert.equal(readFileSync(join(dir, "dir/b.txt"), "utf8"), "kept\n");
  assert.equal(existsSync(`${dir}.tar`), false);

  await removeWorktree(readOnly, repo.dir, dir);
  assert.equal(existsSync(dir), false);
  assert.equal(git(repo.dir, "status", "--porcelain"), "");
});
