// Thresholds and per-project settings. Defaults follow the new-code conditions of SonarQube's
// "Sonar way" gate (zero new issues, 80% coverage, 3% duplication); a repo overrides them in
// .thulu/gates.json, the project's gate profile (profile.js) overrides the repo, and flags override
// all three.

import { tryJSON } from "./util.js";

export const CONFIG_PATH = ".thulu/gates.json";

// Licenses that never block. With no licenses_allow in the repo, anything outside this list is a
// warning; once the repo declares its own list, a license outside it blocks.
export const DEFAULT_LICENSES = [
  "MIT",
  "MIT-0",
  "Apache-2.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "ISC",
  "0BSD",
  "Unlicense",
  "CC0-1.0",
  "Zlib",
  "BSL-1.0",
  "Python-2.0",
  "PSF-2.0",
  "MPL-2.0",
  "BlueOak-1.0.0",
];

export function defaults() {
  return {
    coverage_min: 80,
    coverage_warn: 90,
    coverage_min_lines: 20,
    dup_max: 3,
    dup_min_tokens: 50,
    complexity_max: 15,
    mutation_min_efficacy: 70,
    new_package_days: 30,
    critical: [],
    licenses_allow: null,
    semgrep: { configs: ["p/default"], exclude_rules: [] },
    perf: { threshold_pct: 10, rounds: 10, alpha: 0.05, command: null },
    timeouts: {
      default: 600,
      tests: 1800,
      typecheck: 900,
      mutation: 600,
      perf: 1800,
      // A test suite that prints nothing and reports no test for this long is cut as stuck; 0
      // turns it off.
      test_stall: 300,
    },
    commands: {},
    coverage_files: [],
  };
}

function merge(base, over) {
  if (!over || typeof over !== "object" || Array.isArray(over)) return over === undefined ? base : over;
  const out = { ...base };
  for (const [k, v] of Object.entries(over)) {
    out[k] = base && typeof base[k] === "object" && !Array.isArray(base[k]) && base[k] !== null ? merge(base[k], v) : v;
  }
  return out;
}

// loadConfig reads the repo config from the base revision first: a branch cannot loosen its own
// gates. Only when base has none does the working tree's copy apply (and the suppressions check
// flags the new file as a gate-config change). The profile comes from Thulu, not the branch, so it
// merges over the repo's file; accept_unverified is read only from it, because accepting a check
// that did not finish is a person's decision recorded in Thulu.
export function loadConfig({ baseText, headText, flags, profile = null }) {
  let cfg = defaults();
  const fileText = baseText ?? headText;
  const fromFile = fileText ? tryJSON(fileText) : undefined;
  const warnings = [];
  if (fileText && fromFile === undefined) warnings.push("config_unparsable");
  if (fromFile && typeof fromFile === "object" && !Array.isArray(fromFile)) {
    const rest = { ...fromFile };
    if (rest.accept_unverified !== undefined) warnings.push("config_accept_ignored");
    delete rest.accept_unverified;
    cfg = merge(cfg, rest);
  }
  cfg.accept_unverified = undefined;
  if (profile) {
    const rest = { ...profile };
    delete rest.accept_unverified;
    cfg = merge(cfg, rest);
    cfg.accept_unverified = profile.accept_unverified;
  }
  if (flags.coverageMin !== undefined) cfg.coverage_min = flags.coverageMin;
  if (flags.dupMax !== undefined) cfg.dup_max = flags.dupMax;
  if (flags.critical?.length) cfg.critical = [...new Set([...(cfg.critical || []), ...flags.critical])];
  cfg.source = baseText ? "base" : headText ? "head" : "defaults";
  return { cfg, warnings };
}
