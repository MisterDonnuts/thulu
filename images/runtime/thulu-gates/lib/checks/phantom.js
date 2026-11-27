// phantom_deps: every dependency the diff adds or bumps has to exist in its public registry
// (models invent package names: Spracklen et al., USENIX Security 2025). A missing package or
// version blocks; a package first published fewer than new_package_days ago warns (the
// slopsquatting signal); an unreachable registry warns.

import { dirname, join } from "node:path";

import { changedDeps, exactVersion } from "../deps.js";
import { t } from "../i18n.js";
import { lookup, matchGoPrivate } from "../registry.js";
import { readText } from "../util.js";
import { newResult } from "./common.js";

export const id = "phantom_deps";
const MAX_LOOKUPS = 60;

// privateNpmScopes reads .npmrc files: a scope with its own registry, or a replaced default
// registry, is not checkable against registry.npmjs.org.
export function npmPrivacy(texts) {
  const scopes = new Set();
  let customDefault = false;
  for (const text of texts) {
    for (const line of (text || "").split("\n")) {
      const m = /^\s*(@[\w.-]+):registry\s*=\s*(\S+)/.exec(line);
      if (m) scopes.add(m[1]);
      const d = /^\s*registry\s*=\s*(\S+)/.exec(line);
      if (d && !/registry\.npmjs\.org/.test(d[1])) customDefault = true;
    }
  }
  return { scopes, customDefault };
}

function goSumHas(ctx, dep) {
  const sum = readText(join(ctx.root, dirname(dep.file), "go.sum")) || "";
  return sum.split("\n").some((l) => l.startsWith(`${dep.name} ${dep.version} `) || l.startsWith(`${dep.name} ${dep.version}/go.mod `));
}

export async function run(ctx) {
  const res = newResult(id);
  res.tool = "thulu-gates/registry";
  const deps = await changedDeps(ctx);
  res.metrics.changed = deps.length;
  if (!deps.length) {
    res.status = "pass";
    res.summary = t("deps.no_changes");
    return res;
  }
  const npmrc = npmPrivacy([readText(join(ctx.root, ".npmrc")), ...ctx.projects.map((p) => readText(join(ctx.root, p.dir, ".npmrc")))]);
  const extraPyIndex = deps.some((d) => d.ecosystem === "pypi" && /--(extra-)?index-url/.test(readText(join(ctx.root, d.file)) || ""));
  // Private Go modules: GOPRIVATE may live in `go env -w` (the go env file), not in the process
  // environment the registry lookup reads.
  let goPrivate = [process.env.GOPRIVATE, process.env.GONOPROXY].filter(Boolean).join(",");
  const go = deps.some((d) => d.ecosystem === "go") ? ctx.toolbox.find("go") : null;
  if (go) {
    const r = await ctx.run(go, ["env", "GOPRIVATE", "GONOPROXY"], { cwd: ctx.root, timeoutMs: 30_000 });
    if (r.code === 0) goPrivate = [goPrivate, ...r.stdout.split("\n").map((l) => l.trim())].filter(Boolean).join(",");
  }
  const unreachable = [];
  const skipped = [];
  const rows = [];
  for (const dep of deps.slice(0, MAX_LOOKUPS)) {
    if (dep.ecosystem === "npm" && (npmrc.customDefault || (dep.name.startsWith("@") && npmrc.scopes.has(dep.name.split("/")[0])))) {
      skipped.push(dep.name);
      continue;
    }
    if (dep.ecosystem === "go" && matchGoPrivate(dep.name, goPrivate)) {
      skipped.push(dep.name);
      continue;
    }
    const exact = exactVersion(dep.ecosystem, dep.version);
    const info = await ctx.lookup({ ...dep, name: dep.registryName || dep.name, exact }, lookup);
    rows.push({ ecosystem: dep.ecosystem, name: dep.name, version: dep.version, exists: info.exists, created: info.created || null });
    const where = { file: dep.file, line: dep.line };
    if (info.exists === null) {
      if (info.note === "private") skipped.push(dep.name);
      else unreachable.push(`${dep.name} (${info.error || info.note})`);
      continue;
    }
    // The proxy answers 404/410 for a module it cannot fetch, private ones included. If go.sum
    // already carries its hash, the go command downloaded it: it exists, just not publicly.
    if (info.exists === false && dep.ecosystem === "go" && goSumHas(ctx, dep)) {
      skipped.push(dep.name);
      continue;
    }
    if (info.exists === false) {
      const soft = dep.ecosystem === "pypi" && extraPyIndex;
      res.findings.push({
        rule: "package-not-found",
        severity: soft ? "medium" : "critical",
        ...where,
        message: t("deps.not_found", { name: dep.name, ecosystem: dep.ecosystem }),
        blocking: !soft,
      });
      continue;
    }
    if (info.versionExists === false) {
      res.findings.push({ rule: "version-not-found", severity: "high", ...where, message: t("deps.version_not_found", { name: dep.name, version: exact }), blocking: true });
    }
    if (info.created) {
      const days = (ctx.now() - Date.parse(info.created)) / 86_400_000;
      if (days < ctx.cfg.new_package_days) {
        res.findings.push({
          rule: "recently-published",
          severity: "medium",
          ...where,
          message: t("deps.recent", { name: dep.name, days: Math.max(0, Math.floor(days)), min: ctx.cfg.new_package_days }),
          blocking: false,
        });
      }
    }
  }
  res.metrics.packages = rows;
  if (skipped.length) res.metrics.private_skipped = skipped;
  if (unreachable.length) res.metrics.unreachable = unreachable;
  if (deps.length > MAX_LOOKUPS) res.metrics.not_checked = deps.length - MAX_LOOKUPS;
  const blocking = res.findings.filter((f) => f.blocking).length;
  if (blocking) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("deps.phantom_fail", { count: blocking });
  } else if (res.findings.length || unreachable.length) {
    res.status = "warn";
    res.summary = unreachable.length ? t("deps.unreachable", { count: unreachable.length }) : t("common.warning_findings", { count: res.findings.length });
  } else {
    res.status = "pass";
    res.summary = t("deps.all_exist", { count: rows.length });
  }
  return res;
}
