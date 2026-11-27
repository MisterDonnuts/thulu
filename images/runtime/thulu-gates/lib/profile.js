// The project's gate profile: settings the Thulu operator keeps for a project, outside the client
// repo. Thulu writes it to a file and names it in $THULU_GATES_PROFILE (or --profile); it merges
// over .thulu/gates.json and under the flags. Because a person outside the branch owns it, it may
// relax the repo's gates, and only it can accept a check that cannot be verified
// ("accept_unverified").
//
// The file holds one profile, or one per project of the run:
//   { "schema": 1, "projects": { "<project directory name>": { ...profile } } }
// A run picks the entry named like the repo's directory; a file with a single entry applies to
// whatever repo the run is in.

import { existsSync, readFileSync } from "node:fs";
import { basename } from "node:path";

import { tryJSON } from "./util.js";

const isObject = (v) => v !== null && typeof v === "object" && !Array.isArray(v);

// loadProfile returns { profile, path, entry, warnings }. profile is null when there is none or it
// cannot be read; a warning names why. A broken profile never stops the run: without it the gates
// are only stricter.
export function loadProfile({ flag, env = process.env, root, resolvePath = (p) => p }) {
  const raw = flag || env.THULU_GATES_PROFILE || "";
  const out = { profile: null, path: null, entry: null, warnings: [] };
  if (!raw) return out;
  const path = resolvePath(raw);
  out.path = path;
  if (!existsSync(path)) {
    out.warnings.push({ key: "profile_missing", vars: { path } });
    return out;
  }
  let doc;
  try {
    doc = tryJSON(readFileSync(path, "utf8"));
  } catch {
    doc = undefined;
  }
  if (!isObject(doc)) {
    out.warnings.push({ key: "profile_unparsable", vars: { path } });
    return out;
  }
  if (isObject(doc.projects)) {
    const name = basename(root);
    const names = Object.keys(doc.projects);
    const pick = Object.hasOwn(doc.projects, name) ? name : names.length === 1 ? names[0] : null;
    if (pick === null) {
      if (names.length) out.warnings.push({ key: "profile_no_entry", vars: { path, name, names: names.join(", ") } });
      return out;
    }
    if (!isObject(doc.projects[pick])) {
      out.warnings.push({ key: "profile_unparsable", vars: { path } });
      return out;
    }
    out.entry = pick;
    out.profile = { ...doc.projects[pick] };
  } else {
    out.profile = { ...doc };
    delete out.profile.schema;
    delete out.profile.projects;
  }
  return out;
}

// acceptedChecks validates accept_unverified: { "<check id>": "<reason a person wrote>" }. Unknown
// ids and empty reasons are dropped with a warning, so a typo never accepts the wrong check.
export function acceptedChecks(value, checkIds) {
  const accepted = {};
  const warnings = [];
  if (value === undefined || value === null) return { accepted, warnings };
  if (!isObject(value)) {
    warnings.push({ key: "accept_not_object", vars: {} });
    return { accepted, warnings };
  }
  for (const [id, reason] of Object.entries(value)) {
    if (!checkIds.includes(id)) {
      warnings.push({ key: "accept_unknown_check", vars: { id, ids: checkIds.join(", ") } });
      continue;
    }
    const why = typeof reason === "string" ? reason.trim() : "";
    if (!why) {
      warnings.push({ key: "accept_no_reason", vars: { id } });
      continue;
    }
    accepted[id] = why;
  }
  return { accepted, warnings };
}
