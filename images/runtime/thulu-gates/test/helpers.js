// Test fixtures: throwaway git repos and a fake command runner, so tests need git and nothing
// else (no golangci-lint, semgrep, gitleaks...).

import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";

import { run as realRun } from "../lib/exec.js";

// Git must not read the host's config: no credential helper (the Xcode git opens a keychain
// dialog), no signing, no identity from ~/.gitconfig.
const GIT_ENV = {
  GIT_CONFIG_GLOBAL: "/dev/null",
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_AUTHOR_NAME: "Thulu Test",
  GIT_AUTHOR_EMAIL: "test@example.invalid",
  GIT_COMMITTER_NAME: "Thulu Test",
  GIT_COMMITTER_EMAIL: "test@example.invalid",
  GIT_TERMINAL_PROMPT: "0",
};
Object.assign(process.env, GIT_ENV);

// Base results and quarantines go to a cache of this test process, never the machine's.
const CACHE = mkdtempSync(join(tmpdir(), "thulu-gates-cache-test-"));
process.env.THULU_GATES_CACHE = CACHE;
delete process.env.THULU_GATES_QUARANTINE;
delete process.env.THULU_GATES_PROFILE;
delete process.env.THULU_GATES_REUSE;
delete process.env.THULU_GATES_HOME;
delete process.env.THULU_GATES_HANDED;
process.on("exit", () => rmSync(CACHE, { recursive: true, force: true }));

// isBase tells a fake tool's call on the base checkout from the one on the repo.
export function isBase(opts) {
  return /thulu-gates-base-/.test(opts?.cwd || "");
}

export function git(dir, ...args) {
  return execFileSync("git", args, { cwd: dir, env: { ...process.env, ...GIT_ENV }, encoding: "utf8" });
}

export function writeFiles(dir, files) {
  for (const [p, content] of Object.entries(files)) {
    const abs = join(dir, p);
    if (content === null) {
      rmSync(abs, { force: true });
      continue;
    }
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, content);
  }
}

// makeRepo commits baseFiles on main, points origin/main at it and checks out a feature branch.
export function makeRepo(baseFiles, t) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-test-"));
  if (t) t.after(() => rmSync(dir, { recursive: true, force: true }));
  git(dir, "init", "-q", "-b", "main");
  writeFiles(dir, baseFiles);
  git(dir, "add", "-A");
  git(dir, "commit", "-qm", "base");
  git(dir, "update-ref", "refs/remotes/origin/main", "HEAD");
  git(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main");
  git(dir, "checkout", "-qb", "feature");
  return {
    dir,
    commit(files, msg = "change") {
      writeFiles(dir, files);
      git(dir, "add", "-A");
      git(dir, "commit", "-qm", msg);
    },
    write(files) {
      writeFiles(dir, files);
    },
  };
}

// fakeRunner answers commands from handlers keyed by binary name; git goes to the real git.
// A handler gets (args, opts) and returns {code, stdout, stderr} (or a promise of it).
export function fakeRunner(handlers = {}) {
  const calls = [];
  const run = async (cmd, args = [], opts = {}) => {
    const name = basename(cmd);
    if (name === "git") return realRun(cmd, args, { ...opts, env: { ...GIT_ENV, ...(opts.env || {}) } });
    calls.push({ cmd: name, args, cwd: opts.cwd, env: opts.env });
    const h = handlers[name];
    if (!h) return { code: 127, notFound: true, stdout: "", stderr: `${name}: not found`, durationMs: 0, timedOut: false };
    const r = (await h(args, opts)) || {};
    return { code: 0, stdout: "", stderr: "", durationMs: 1, timedOut: false, ...r };
  };
  run.calls = calls;
  return run;
}

export function fakeWhich(names) {
  const set = new Set(names);
  return (bin) => (set.has(bin) ? `/fake/bin/${bin}` : null);
}

export function outDir(t) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-out-"));
  if (t) t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

// noNetwork is a fetch that fails like an offline sandbox.
export async function noNetwork() {
  const err = new TypeError("fetch failed");
  err.cause = { code: "ENOTFOUND" };
  throw err;
}

export function jsonResponse(status, body) {
  return { status, ok: status >= 200 && status < 300, text: async () => (typeof body === "string" ? body : JSON.stringify(body)) };
}

export function collect() {
  let text = "";
  const fn = (s) => {
    text += s;
  };
  fn.text = () => text;
  return fn;
}
