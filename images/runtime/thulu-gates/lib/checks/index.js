// Check order matters: tests leaves the coverage files coverage_new reads.

import * as coverage from "./coverage.js";
import * as deps from "./deps.js";
import * as duplication from "./duplication.js";
import * as licenses from "./licenses.js";
import * as lint from "./lint.js";
import * as mutation from "./mutation.js";
import * as perf from "./perf.js";
import * as phantom from "./phantom.js";
import * as sast from "./sast.js";
import * as secrets from "./secrets.js";
import * as suppressions from "./suppressions.js";
import * as tests from "./tests.js";
import * as typecheck from "./typecheck.js";

export const CHECKS = [tests, coverage, duplication, lint, typecheck, sast, secrets, deps, licenses, phantom, suppressions, mutation, perf];

export const CHECK_IDS = CHECKS.map((c) => c.id);

// Opt-in checks: they cost minutes and only run with their flag (or when --only names them).
export const OPT_IN = new Set(["mutation", "perf"]);
