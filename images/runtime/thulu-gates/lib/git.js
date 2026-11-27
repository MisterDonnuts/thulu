// Git plumbing: base and head, the line-level diff, files at a revision and a base worktree.

import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const GIT_TIMEOUT = 120_000;

// The parser strips a/ and b/; diff.noprefix or diff.mnemonicPrefix in the user's config would
// change them (c/, w/, i/), so every diff names its prefixes.
const PREFIXES = ["--src-prefix=a/", "--dst-prefix=b/"];

async function git(run, root, args, opts = {}) {
  const r = await run("git", args, { cwd: root, timeoutMs: opts.timeoutMs ?? GIT_TIMEOUT });
  if (r.code !== 0 && !opts.allowFail) {
    const err = new Error(`git ${args.join(" ")}: ${(r.stderr || r.error || "").trim()}`);
    err.gitResult = r;
    throw err;
  }
  return r;
}

export async function repoRoot(run, cwd) {
  const r = await git(run, cwd, ["rev-parse", "--show-toplevel"]);
  return r.stdout.trim();
}

export async function headSha(run, root) {
  const r = await git(run, root, ["rev-parse", "HEAD"]);
  return r.stdout.trim();
}

export async function revParse(run, root, ref) {
  const r = await git(run, root, ["rev-parse", "--verify", "--quiet", `${ref}^{commit}`], { allowFail: true });
  return r.code === 0 ? r.stdout.trim() : null;
}

// defaultBranch answers the name origin calls its default branch: origin/HEAD when the clone set
// it, else the first of main/master that exists remotely, then locally.
export async function defaultBranch(run, root) {
  const sym = await git(run, root, ["symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"], { allowFail: true });
  if (sym.code === 0) {
    const name = sym.stdout.trim().replace(/^refs\/remotes\/origin\//, "");
    if (name) return name;
  }
  for (const b of ["main", "master"]) {
    if (await revParse(run, root, `refs/remotes/origin/${b}`)) return b;
  }
  for (const b of ["main", "master"]) {
    if (await revParse(run, root, `refs/heads/${b}`)) return b;
  }
  return "main";
}

// mergeBase computes git merge-base <ref> HEAD. A shallow clone gets one --deepen fetch before
// giving up, since the merge-base usually sits just past the shallow boundary.
export async function mergeBase(run, root, ref) {
  let r = await git(run, root, ["merge-base", ref, "HEAD"], { allowFail: true });
  if (r.code === 0) return r.stdout.trim();
  const shallow = await git(run, root, ["rev-parse", "--is-shallow-repository"], { allowFail: true });
  if (shallow.stdout.trim() === "true") {
    const branch = ref.replace(/^origin\//, "");
    await git(run, root, ["fetch", "--no-tags", "--deepen=500", "origin", branch], {
      allowFail: true,
      timeoutMs: 300_000,
    });
    r = await git(run, root, ["merge-base", ref, "HEAD"], { allowFail: true });
    if (r.code === 0) return r.stdout.trim();
  }
  throw new Error(`git merge-base ${ref} HEAD: ${(r.stderr || "no common ancestor").trim()}`);
}

export async function isDirty(run, root) {
  const r = await git(run, root, ["status", "--porcelain", "--untracked-files=no"], { allowFail: true });
  return r.stdout.trim() !== "";
}

// parseUnifiedDiff reads `git diff --unified=0` output into path -> {status, added: Set<line>,
// addedCount, deletedCount, removed: [text]}. Deleted files are left out: nothing in them is new
// code.
export function parseUnifiedDiff(text) {
  const files = new Map();
  let cur = null;
  let newLine = 0;
  let oldPath = null;
  let status = "M";
  // File headers only appear between "diff --git" and the first hunk; inside a hunk a removed
  // line "-- x" reads "--- x" and must not be taken for a header.
  let inHeader = false;
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git ")) {
      cur = null;
      oldPath = null;
      status = "M";
      inHeader = true;
      continue;
    }
    if (inHeader) {
      if (line.startsWith("new file mode")) status = "A";
      else if (line.startsWith("rename from ")) status = "R";
      else if (line.startsWith("--- ")) oldPath = line === "--- /dev/null" ? null : headerPath(line).replace(/^a\//, "");
      else if (line.startsWith("+++ ")) {
        if (line !== "+++ /dev/null") {
          const p = headerPath(line).replace(/^b\//, "");
          cur = { path: p, oldPath, status: oldPath === null ? "A" : status, added: new Set(), addedCount: 0, deletedCount: 0, removed: [], hunks: [] };
          files.set(p, cur);
        }
      } else if (line.startsWith("@@")) inHeader = false;
      if (inHeader) continue;
    }
    if (!cur) continue;
    if (line.startsWith("@@")) {
      const m = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(line);
      if (m) {
        newLine = Number(m[3]);
        cur.hunks.push([Number(m[1]), m[2] === undefined ? 1 : Number(m[2]), newLine, m[4] === undefined ? 1 : Number(m[4])]);
      }
      continue;
    }
    if (line.startsWith("+")) {
      cur.added.add(newLine);
      cur.addedCount++;
      newLine++;
    } else if (line.startsWith("-")) {
      cur.deletedCount++;
      if (cur.removed.length < 2000) cur.removed.push(line.slice(1));
    } else if (line.startsWith(" ")) {
      newLine++;
    }
  }
  return files;
}

// baseLine maps a line of the file at head to the line it was at base: lines between hunks shift
// by what the hunks before them added and removed; a line inside a hunk maps to where that hunk
// starts at base. null for a file base did not have.
export function baseLine(d, n) {
  if (!d || d.status === "A") return null;
  let shift = 0;
  for (const [oldStart, oldCount, newStart, newCount] of d.hunks || []) {
    // A hunk that only removes lines sits after line newStart.
    if (n < (newCount ? newStart : newStart + 1)) break;
    if (n < newStart + newCount) return Math.max(1, oldCount ? oldStart : oldStart + 1);
    shift += oldCount - newCount;
  }
  return n + shift;
}

// headerPath reads the path of a ---/+++ line. Git appends a tab to a path that contains a space
// (so GNU patch can parse it), which must not end up in the path.
function headerPath(line) {
  return unquote(line.slice(4).replace(/\t$/, ""));
}

function unquote(p) {
  if (p.startsWith('"') && p.endsWith('"')) {
    try {
      return JSON.parse(p);
    } catch {
      return p.slice(1, -1);
    }
  }
  return p;
}

// diffAgainst diffs the working tree (tracked changes, committed or not) against base, and adds
// untracked files as wholly new. That is what the tests and linters will see on disk.
export async function diffAgainst(run, root, base) {
  const r = await git(run, root, [
    "-c",
    "core.quotePath=false",
    "diff",
    "--unified=0",
    "--no-color",
    "--no-ext-diff",
    "--find-renames",
    ...PREFIXES,
    base,
  ]);
  const files = parseUnifiedDiff(r.stdout);
  const untracked = await git(run, root, ["ls-files", "--others", "--exclude-standard", "-z"], { allowFail: true });
  for (const p of untracked.stdout.split("\0").filter(Boolean)) {
    if (files.has(p)) continue;
    let text;
    try {
      text = readFileSync(join(root, p), "utf8");
    } catch {
      continue;
    }
    if (text.includes("\0")) continue;
    const n = text.split("\n").length - (text.endsWith("\n") ? 1 : 0);
    const added = new Set();
    for (let i = 1; i <= n; i++) added.add(i);
    files.set(p, { path: p, oldPath: null, status: "A", added, addedCount: n, deletedCount: 0, removed: [], untracked: true });
  }
  return files;
}

// dirtyFiles lists tracked files with uncommitted changes.
export async function dirtyFiles(run, root) {
  const r = await git(run, root, ["diff", "--name-only", "-z", "HEAD"], { allowFail: true });
  return r.stdout.split("\0").filter(Boolean);
}

// rawDiff is the same diff as text, for tools that take a patch file (cargo mutants --in-diff).
export async function rawDiff(run, root, base) {
  const r = await git(run, root, ["diff", "--no-color", "--no-ext-diff", ...PREFIXES, base]);
  return r.stdout;
}

// showFile returns path's content at rev, or null when it did not exist there.
export async function showFile(run, root, rev, path) {
  const r = await git(run, root, ["show", `${rev}:${path}`], { allowFail: true });
  return r.code === 0 ? r.stdout : null;
}

export async function lsFiles(run, root) {
  const r = await git(run, root, ["ls-files", "-z"], { allowFail: true });
  return r.stdout.split("\0").filter(Boolean);
}

// addWorktree checks base out next to the repo; the caller removes it with removeWorktree. When
// git cannot register a worktree (Docker mounts the project's .git read-only), it exports base's
// tree with git archive instead, which only reads objects. The export is not a repo and leaves out
// paths marked export-ignore; the checks that use it only run tools over the files.
export async function addWorktree(run, root, base) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-base-"));
  const wt = await git(run, root, ["worktree", "add", "--detach", "--force", dir, base], { timeoutMs: 300_000, allowFail: true });
  if (wt.code === 0) return dir;
  const tar = `${dir}.tar`;
  try {
    await git(run, root, ["archive", "--format=tar", "-o", tar, base], { timeoutMs: 300_000 });
    const x = await run("tar", ["-xf", tar, "-C", dir], { timeoutMs: 300_000 });
    if (x.code !== 0) throw new Error(`tar -xf: ${(x.stderr || x.error || "").toString().trim()}`);
  } catch (err) {
    rmSync(dir, { recursive: true, force: true });
    throw new Error(`git worktree add: ${(wt.stderr || "").trim()}; ${err.message}`);
  } finally {
    rmSync(tar, { force: true });
  }
  return dir;
}

export async function removeWorktree(run, root, dir) {
  await git(run, root, ["worktree", "remove", "--force", dir], { allowFail: true });
  rmSync(dir, { recursive: true, force: true });
  await git(run, root, ["worktree", "prune"], { allowFail: true });
}
