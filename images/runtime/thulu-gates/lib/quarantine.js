// Quarantine: tests that hang or fail on the base commit too, so they say nothing about the change.
// The list lives outside the client repo, one file per repo (--quarantine, $THULU_GATES_QUARANTINE,
// or <cache dir>/<project key>/quarantine.json). A run leaves quarantined tests out of the suite
// where the runner can exclude a single test, and reports each one as not run. Entries only come
// from runs that saw the test break on base; a person deletes an entry to bring the test back.

import { existsSync, readFileSync } from "node:fs";

import { writeJSON } from "./cache.js";
import { tryJSON } from "./util.js";

export function loadQuarantine(path) {
  const state = { path, tests: [], added: [], error: null };
  if (existsSync(path)) {
    const doc = tryJSON(readFileSync(path, "utf8"));
    if (!doc || !Array.isArray(doc.tests)) state.error = "unreadable";
    else state.tests = doc.tests.filter((e) => e && typeof e.id === "string");
  }
  return {
    get path() {
      return state.path;
    },
    get error() {
      return state.error;
    },
    get added() {
      return state.added;
    },
    // forProject lists the entries of one project of the repo (its directory) and runner.
    forProject(dir, runner) {
      return state.tests.filter((e) => (e.project || "") === (dir || "") && (!runner || !e.runner || e.runner === runner));
    },
    add(entry) {
      if (state.tests.some((e) => e.id === entry.id && (e.project || "") === (entry.project || ""))) return false;
      const full = { ...entry, added_at: new Date().toISOString() };
      state.tests.push(full);
      state.added.push(full);
      return true;
    },
    // save writes the entries this run added on top of what is on disk now, so two runs of the
    // same repo do not erase each other's. An unreadable file is left alone.
    save() {
      if (!state.added.length || state.error) return;
      let onDisk = [];
      if (existsSync(path)) {
        const doc = tryJSON(readFileSync(path, "utf8"));
        if (!doc || !Array.isArray(doc.tests)) return;
        onDisk = doc.tests;
      }
      const merged = [...onDisk];
      for (const e of state.added) if (!merged.some((m) => m.id === e.id && (m.project || "") === (e.project || ""))) merged.push(e);
      writeJSON(path, { schema: 1, tests: merged });
    },
  };
}
