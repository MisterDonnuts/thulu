// Report reuse: a run on a clean tree stores its check results in the cache, keyed by everything
// that changes them (head, base, this tool's version, the effective config with the profile and
// flags folded in, the quarantine). `--reuse` (or THULU_GATES_REUSE=1) hands back the stored
// results when the key matches, so a second stage on the head the first one measured does not run
// the suite again. With --only, the listed checks run again and the rest come from the store.
//
// What is stored is each check's result before accept_unverified applies: acceptance is applied
// again on every report, so changing a reason in the profile never needs a rerun.

import { readFileSync } from "node:fs";
import { join } from "node:path";

import { writeJSON } from "./cache.js";
import { sha256, tryJSON } from "./util.js";

// Bumped when the stored shape changes, so old entries are not read.
const REPORT_CACHE_SCHEMA = 1;

// stable serializes with sorted keys, so two configs equal in content hash the same.
export function stable(v) {
  if (Array.isArray(v)) return `[${v.map(stable).join(",")}]`;
  if (v && typeof v === "object") {
    return `{${Object.keys(v)
      .filter((k) => v[k] !== undefined)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${stable(v[k])}`)
      .join(",")}}`;
  }
  return JSON.stringify(v ?? null);
}

// configHash covers what decides a check's result. accept_unverified is applied after the checks
// and is left out; so are where the config came from and the profile's path, which on a runner is
// a new folder every run.
export function configHash(cfg) {
  const rest = { ...cfg };
  for (const k of ["accept_unverified", "source", "profile"]) delete rest[k];
  return sha256(stable(rest)).slice(0, 16);
}

export function quarantineHash(path) {
  let tests = [];
  try {
    const doc = tryJSON(readFileSync(path, "utf8"));
    if (doc && Array.isArray(doc.tests)) tests = doc.tests.map((e) => `${e.project || ""}\0${e.id}`).sort();
  } catch {
    // no file: no quarantine
  }
  return sha256(JSON.stringify(tests)).slice(0, 16);
}

export function reuseKey({ head, base, version, cfg, quarantinePath }) {
  return sha256(JSON.stringify([REPORT_CACHE_SCHEMA, head, base, version, configHash(cfg), quarantineHash(quarantinePath)])).slice(0, 40);
}

// createReportStore keeps one entry per key under <dir>/<key>.json. A failed read or write only
// costs a full run.
export function createReportStore({ dir, log = () => {} }) {
  const pathOf = (key) => join(dir, `${key}.json`);
  return {
    get(key) {
      try {
        const doc = tryJSON(readFileSync(pathOf(key), "utf8"));
        return doc && doc.schema === REPORT_CACHE_SCHEMA && doc.key === key && Array.isArray(doc.checks) ? doc : undefined;
      } catch {
        return undefined;
      }
    },
    set(key, entry) {
      try {
        writeJSON(pathOf(key), { schema: REPORT_CACHE_SCHEMA, key, ...entry });
      } catch (err) {
        log(`thulu-gates: report cache not written: ${err.message}`);
      }
    },
  };
}

// plan decides, check by check, whether it runs, comes from the stored entry or is left out.
// wanted(id) says whether this run would run the check without reuse. With --only, the listed
// checks run and every other check the entry measured is reused. Without it, a check runs again
// when the entry did not measure it or it ended in error (a time-out or a crash says nothing about
// the code, so it is worth another try), unless the profile accepts that check unverified: a person
// already decided that result is enough, and running a suite that hangs again would only cost the
// same half hour. coverage_new reads the coverage files tests leaves, which
// are not stored, so running coverage_new runs tests too.
export function plan({ ids, wanted, only, entry, accepted = {} }) {
  const stored = new Map((entry?.checks || []).map((c) => [c.id, c]));
  const measured = new Set(entry?.ran || []);
  const reusable = (id) => measured.has(id) && stored.has(id) && (stored.get(id).status !== "error" || Boolean(accepted[id]));
  const out = new Map();
  for (const id of ids) {
    if (!entry) out.set(id, wanted(id) ? "run" : "leave");
    else if (only) out.set(id, only.includes(id) ? "run" : reusable(id) ? "reuse" : "leave");
    else out.set(id, !wanted(id) ? "leave" : reusable(id) ? "reuse" : "run");
  }
  if (out.get("coverage_new") === "run" && out.get("tests") === "reuse") out.set("tests", "run");
  return { steps: out, stored };
}
