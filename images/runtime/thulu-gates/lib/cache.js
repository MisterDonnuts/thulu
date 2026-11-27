// The gate's own state, kept outside the client repo: base-commit results (so a second run on the
// same base does not compute them again) and the quarantine of tests that hang or fail on base.
// Everything lives under <cache dir>/<project key>/. The cache dir is --cache-dir, then
// $THULU_GATES_CACHE, then /cache/thulu-gates when the sandbox mounts the workspace cache at
// /cache, then <tmp>/thulu-gates-cache.

import { accessSync, constants, mkdirSync, readFileSync, renameSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";

import { sha256, tryJSON } from "./util.js";

// Bumped when the shape of a cached base result changes, so old entries are not read.
const CACHE_SCHEMA = 1;

function writableDir(p) {
  try {
    if (!statSync(p).isDirectory()) return false;
    accessSync(p, constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

export function resolveCacheDir(flag, env = process.env, cwd = process.cwd()) {
  if (flag) return resolve(cwd, flag);
  if (env.THULU_GATES_CACHE) return resolve(cwd, env.THULU_GATES_CACHE);
  if (writableDir("/cache")) return "/cache/thulu-gates";
  return join(tmpdir(), "thulu-gates-cache");
}

// projectKey names the repo the same way from every clone and worktree of it: origin's URL without
// credentials or scheme, else the repo directory's name. A short hash keeps two URLs that slug the
// same apart.
export async function projectKey(run, root) {
  const r = await run("git", ["config", "--get", "remote.origin.url"], { cwd: root, timeoutMs: 20_000 });
  const url = r.code === 0 ? r.stdout.trim() : "";
  const id = url
    ? url
        .replace(/^[a-z+]+:\/\//i, "")
        .replace(/^[^@/]+@/, "")
        .replace(/\.git$/, "")
        .replace(/:/g, "/")
        .toLowerCase()
    : basename(root);
  const slug = id.replace(/[^a-z0-9._-]+/gi, "-").replace(/^-+|-+$/g, "").slice(-60) || "repo";
  return `${slug}-${sha256(id).slice(0, 8)}`;
}

// writeJSON writes through a temporary file and a rename, so a run killed halfway never leaves a
// truncated file that the next run would read.
export function writeJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true });
  const tmp = `${path}.${process.pid}.tmp`;
  writeFileSync(tmp, JSON.stringify(value, null, 2) + "\n");
  renameSync(tmp, path);
}

// createBaseCache stores one JSON file per base result, keyed by the base commit, the check and
// whatever else changes the answer (tool and version, project, file, thresholds). A failed read
// or write only costs a recomputation.
export function createBaseCache({ dir, base, log = () => {} }) {
  const root = join(dir, "base", base);
  const pathOf = (parts) => {
    const [check, ...rest] = parts;
    return join(root, String(check), `${sha256(JSON.stringify([CACHE_SCHEMA, ...rest])).slice(0, 40)}.json`);
  };
  return {
    dir: root,
    get(parts) {
      try {
        const doc = tryJSON(readFileSync(pathOf(parts), "utf8"));
        return doc && doc.schema === CACHE_SCHEMA ? doc.value : undefined;
      } catch {
        return undefined;
      }
    },
    set(parts, value) {
      try {
        writeJSON(pathOf(parts), { schema: CACHE_SCHEMA, key: parts, value });
      } catch (err) {
        log(`thulu-gates: base cache not written: ${err.message}`);
      }
    },
    // memo returns the cached value or computes, stores and returns it. compute returns
    // {value, cache}: cache false keeps a result out of the cache (a base run that broke for
    // reasons outside the code).
    async memo(parts, compute) {
      const hit = this.get(parts);
      if (hit !== undefined) return { value: hit, cached: true };
      const { value, cache = true } = await compute();
      if (cache && value !== undefined) this.set(parts, value);
      return { value, cached: false };
    },
  };
}
