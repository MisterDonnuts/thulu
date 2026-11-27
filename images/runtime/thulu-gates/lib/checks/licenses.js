// licenses: the license of each dependency the diff adds or bumps, from the installed package,
// the registry or deps.dev. With licenses_allow in .thulu/gates.json a runtime dependency outside
// the list blocks; with the built-in list it only warns, since the repo never said what it
// accepts. Unknown licenses warn.

import { join } from "node:path";

import { DEFAULT_LICENSES } from "../config.js";
import { changedDeps, exactVersion } from "../deps.js";
import { t } from "../i18n.js";
import { depsDevLicense, lookup } from "../registry.js";
import { readText, tryJSON } from "../util.js";
import { newResult, settle } from "./common.js";

export const id = "licenses";

// spdxAllowed evaluates an SPDX expression: OR needs one allowed side, AND needs both, and
// "X WITH exception" counts as X. A slash is OR: crates.io still serves "MIT/Apache-2.0" for
// crates published before it required SPDX (smallvec 1.6.0, for one).
export function spdxAllowed(expr, allow) {
  const allowed = new Set(allow.map((x) => x.toLowerCase()));
  const tokens = String(expr)
    .replace(/\//g, " OR ")
    .replace(/[()]/g, (p) => ` ${p} `)
    .trim()
    .split(/\s+/)
    .filter(Boolean);
  let i = 0;
  const atom = () => {
    const tok = tokens[i++];
    if (tok === "(") {
      const v = or();
      i++;
      return v;
    }
    let v = allowed.has(String(tok).toLowerCase().replace(/\+$/, "")) || allowed.has(String(tok).toLowerCase());
    if (tokens[i]?.toUpperCase() === "WITH") i += 2;
    return v;
  };
  const and = () => {
    let v = atom();
    while (tokens[i]?.toUpperCase() === "AND") {
      i++;
      v = atom() && v;
    }
    return v;
  };
  const or = () => {
    let v = and();
    while (tokens[i]?.toUpperCase() === "OR") {
      i++;
      v = and() || v;
    }
    return v;
  };
  return or();
}

function installedNpm(ctx, dep) {
  const dirs = [dep.file.includes("/") ? dep.file.slice(0, dep.file.lastIndexOf("/")) : "", ""];
  for (const d of dirs) {
    const pkg = tryJSON(readText(join(ctx.root, d, "node_modules", dep.name, "package.json")) || "");
    if (pkg) return { version: pkg.version, license: typeof pkg.license === "string" ? pkg.license : pkg.license?.type || null };
  }
  return null;
}

export async function run(ctx) {
  const res = newResult(id);
  res.tool = "thulu-gates/licenses";
  const deps = await changedDeps(ctx);
  if (!deps.length) {
    res.status = "pass";
    res.summary = t("deps.no_changes");
    return res;
  }
  const declared = Array.isArray(ctx.cfg.licenses_allow);
  const allow = declared ? ctx.cfg.licenses_allow : DEFAULT_LICENSES;
  const rows = [];
  for (const dep of deps.slice(0, 60)) {
    let license = null;
    let version = exactVersion(dep.ecosystem, dep.version);
    if (dep.ecosystem === "npm") {
      const inst = installedNpm(ctx, dep);
      if (inst) {
        license = inst.license;
        version = version || inst.version;
      }
    }
    const registryDep = { ...dep, name: dep.registryName || dep.name };
    if (!license) license = (await ctx.lookup({ ...registryDep, exact: version }, lookup)).license || null;
    if (!license && version) license = await depsDevLicense(registryDep, version, { fetchFn: ctx.fetch });
    rows.push({ ecosystem: dep.ecosystem, name: dep.name, version: dep.version, license });
    const where = { file: dep.file, line: dep.line };
    if (!license) {
      res.findings.push({ rule: "license-unknown", severity: "low", ...where, message: t("licenses.unknown", { name: dep.name }), blocking: false });
      continue;
    }
    if (spdxAllowed(license, allow)) continue;
    const blocks = declared && !dep.dev;
    res.findings.push({
      rule: "license-not-allowed",
      severity: blocks ? "high" : "medium",
      ...where,
      message: t(declared ? "licenses.not_allowed" : "licenses.not_in_default", { name: dep.name, license }),
      blocking: blocks,
    });
  }
  res.metrics.packages = rows;
  res.metrics.allowlist = declared ? "repo" : "default";
  return settle(res, {
    failSummary: t("licenses.fail", { count: res.findings.filter((f) => f.blocking).length }),
    passSummary: t("licenses.ok", { count: rows.length }),
  });
}
