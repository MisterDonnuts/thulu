// deps: known vulnerabilities, from every scanner installed (osv-scanner for any lockfile,
// govulncheck for reachable Go code, npm audit, pip-audit, cargo audit). A HIGH or CRITICAL
// vulnerability with a fix, in a dependency this diff adds or bumps (or, for govulncheck, reached
// from changed code), blocks. Other new ones warn; preexisting ones are only counted.

import { existsSync } from "node:fs";
import { join } from "node:path";

import { changedDeps } from "../deps.js";
import { t } from "../i18n.js";
import { firstLine, jsonStream, relTo, tryJSON } from "../util.js";
import { absDir, exec, missingTool, newResult, settle, use } from "./common.js";
import { pyPrefix } from "./tests.js";

export const id = "deps";

const SEV_ORDER = { critical: 4, high: 3, medium: 2, moderate: 2, low: 1, unknown: 0 };

export function cvssToSeverity(score) {
  const n = Number(score);
  if (!Number.isFinite(n)) return "unknown";
  if (n >= 9) return "critical";
  if (n >= 7) return "high";
  if (n >= 4) return "medium";
  return n > 0 ? "low" : "unknown";
}

function normSev(s) {
  const x = String(s || "").toLowerCase();
  if (x === "moderate") return "medium";
  return SEV_ORDER[x] !== undefined ? x : "unknown";
}

export function parseOsv(doc) {
  const out = [];
  for (const res of doc?.results || []) {
    for (const pkg of res.packages || []) {
      const p = pkg.package || {};
      const groupSev = new Map();
      const groupOf = new Map();
      (pkg.groups || []).forEach((g, gi) => {
        for (const id of g.ids || []) {
          groupSev.set(id, cvssToSeverity(g.max_severity));
          groupOf.set(id, gi);
        }
      });
      // One advisory published by several databases (RUSTSEC-2021-0003 and GHSA-43w2-9j62-hq99,
      // PYSEC-2020-96 and GHSA-6757-jp84-gxfx) comes as one group: one finding, not one per id.
      const emitted = new Map();
      for (const v of pkg.vulnerabilities || []) {
        const fixed = (v.affected || []).some((a) => (a.ranges || []).some((r) => (r.events || []).some((e) => e.fixed)));
        const gi = groupOf.get(v.id);
        if (gi !== undefined && emitted.has(gi)) {
          const first = emitted.get(gi);
          first.fixed = first.fixed || fixed;
          if (!first.summary) first.summary = v.summary || "";
          first.aliases.push(v.id);
          continue;
        }
        const sev = groupSev.get(v.id) && groupSev.get(v.id) !== "unknown" ? groupSev.get(v.id) : normSev(v.database_specific?.severity);
        const row = { tool: "osv-scanner", ecosystem: (p.ecosystem || "").toLowerCase(), package: p.name, version: p.version, id: v.id, aliases: [], severity: sev, fixed, summary: v.summary || "", file: relTo("", res.source?.path || "") };
        if (gi !== undefined) emitted.set(gi, row);
        out.push(row);
      }
    }
  }
  return out;
}

export function parseNpmAudit(doc) {
  const out = [];
  for (const [name, v] of Object.entries(doc?.vulnerabilities || {})) {
    const advisories = (v.via || []).filter((x) => typeof x === "object");
    out.push({
      tool: "npm audit",
      ecosystem: "npm",
      package: name,
      version: v.range || "",
      id: advisories.map((a) => a.url?.split("/").pop() || a.source).filter(Boolean).join(",") || `via ${(v.via || []).join(",")}`,
      severity: normSev(v.severity),
      fixed: v.fixAvailable !== false,
      summary: advisories[0]?.title || "",
    });
  }
  return out;
}

export function parseGovulncheck(text) {
  const osv = new Map();
  const out = [];
  for (const ev of jsonStream(text)) {
    if (ev.osv) osv.set(ev.osv.id, ev.osv);
    if (!ev.finding) continue;
    const f = ev.finding;
    const trace = f.trace || [];
    // Symbol-level findings (a called function) are the reachable ones; module- and package-level
    // findings only say the dependency is present.
    if (!trace[0]?.function) continue;
    const frames = trace.filter((fr) => fr.position?.filename);
    out.push({
      tool: "govulncheck",
      ecosystem: "go",
      package: trace[0].module,
      version: trace[0].version || "",
      id: f.osv,
      severity: "unknown",
      fixed: Boolean(f.fixed_version),
      fixedVersion: f.fixed_version || "",
      summary: osv.get(f.osv)?.summary || "",
      callers: frames.map((fr) => ({ file: fr.position.filename, line: fr.position.line })),
    });
  }
  const seen = new Set();
  return out.filter((v) => {
    const k = `${v.id}\0${v.package}`;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

export function parsePipAudit(doc) {
  const out = [];
  const deps = Array.isArray(doc) ? doc : doc?.dependencies || [];
  for (const d of deps) {
    for (const v of d.vulns || []) {
      out.push({ tool: "pip-audit", ecosystem: "pypi", package: String(d.name).toLowerCase().replace(/[-_.]+/g, "-"), version: d.version, id: v.id, severity: "unknown", fixed: (v.fix_versions || []).length > 0, summary: (v.description || "").slice(0, 200) });
    }
  }
  return out;
}

export function parseCargoAudit(doc) {
  return (doc?.vulnerabilities?.list || []).map((v) => ({
    tool: "cargo audit",
    ecosystem: "cargo",
    package: v.package?.name || v.advisory?.package,
    version: v.package?.version || "",
    id: v.advisory?.id,
    severity: v.advisory?.cvss ? "high" : "unknown",
    fixed: (v.versions?.patched || []).length > 0,
    summary: v.advisory?.title || "",
  }));
}

export async function run(ctx) {
  const res = newResult(id);
  if (!ctx.projects.length && !ctx.diff.size) return missingTool(res, "osv-scanner");
  const vulns = [];
  const errors = [];
  let ran = 0;

  const osv = ctx.toolbox.find("osv-scanner");
  if (osv) {
    await use(ctx, res, "osv-scanner", osv);
    const version = await ctx.toolbox.version("osv-scanner", osv);
    const v1 = /\b1\.\d+\.\d+/.test(version);
    const r = await exec(ctx, res, osv, v1 ? ["--format", "json", "-r", "."] : ["scan", "source", "--format", "json", "-r", "."]);
    const doc = tryJSON(r.stdout);
    if (doc) {
      ran++;
      vulns.push(...parseOsv(doc));
    } else if (r.code !== 128) errors.push(`osv-scanner: ${firstLine(r.stderr) || `exit ${r.code}`}`);
  }
  for (const p of ctx.projects) {
    const dir = absDir(ctx, p.dir);
    if (p.stack === "go" && ctx.toolbox.find("govulncheck")) {
      await use(ctx, res, "govulncheck", ctx.toolbox.find("govulncheck"));
      const r = await exec(ctx, res, "govulncheck", ["-format", "json", "./..."], { cwd: dir });
      if (r.code === 0 || r.code === 3) {
        ran++;
        vulns.push(...parseGovulncheck(r.stdout).map((v) => ({ ...v, callers: v.callers.map((c) => ({ ...c, file: relTo(ctx.root, c.file.startsWith("/") ? c.file : join(dir, c.file)) })) })));
      } else errors.push(`govulncheck: ${firstLine(r.stderr) || `exit ${r.code}`}`);
    }
    if ((p.stack === "ts" || p.stack === "js") && !osv && p.manager === "npm" && existsSync(join(dir, "package-lock.json")) && ctx.toolbox.find("npm")) {
      await use(ctx, res, "npm", ctx.toolbox.find("npm"));
      const r = await exec(ctx, res, "npm", ["audit", "--json", "--omit=dev"], { cwd: dir });
      const doc = tryJSON(r.stdout);
      if (doc && !doc.error) {
        ran++;
        vulns.push(...parseNpmAudit(doc));
      } else errors.push(`npm audit: ${firstLine(doc?.error?.summary || r.stderr) || `exit ${r.code}`}`);
    }
    if (p.stack === "python" && !osv && ctx.toolbox.find("pip-audit")) {
      await use(ctx, res, "pip-audit", ctx.toolbox.find("pip-audit"));
      const args = existsSync(join(dir, "requirements.txt")) ? ["-f", "json", "-r", "requirements.txt"] : ["-f", "json"];
      const prefix = existsSync(join(dir, "requirements.txt")) ? [] : pyPrefix(ctx, p);
      const r = prefix.length
        ? await exec(ctx, res, prefix[0], [...prefix.slice(1), "pip-audit", ...args], { cwd: dir })
        : await exec(ctx, res, "pip-audit", args, { cwd: dir });
      const doc = tryJSON(r.stdout);
      if (doc) {
        ran++;
        vulns.push(...parsePipAudit(doc));
      } else errors.push(`pip-audit: ${firstLine(r.stderr) || `exit ${r.code}`}`);
    }
    if (p.stack === "rust" && !osv && ctx.toolbox.find("cargo-audit")) {
      await use(ctx, res, "cargo-audit", ctx.toolbox.find("cargo-audit"));
      const r = await exec(ctx, res, "cargo", ["audit", "--json"], { cwd: dir });
      const doc = tryJSON(r.stdout);
      if (doc) {
        ran++;
        vulns.push(...parseCargoAudit(doc));
      } else errors.push(`cargo audit: ${firstLine(r.stderr) || `exit ${r.code}`}`);
    }
  }
  if (!ran) {
    if (errors.length) {
      res.status = "error";
      res.summary = errors.join("; ");
      return res;
    }
    const suggestions = ["osv-scanner"];
    if (ctx.projects.some((p) => p.stack === "go")) suggestions.push("govulncheck");
    return missingTool(res, suggestions);
  }

  const changed = await changedDeps(ctx);
  // Scanners and manifests spell names differently (PyPI: typing_extensions vs
  // typing-extensions, case), and an npm alias is reported under its real name.
  const norm = (s) => String(s || "").toLowerCase().replace(/[-_.]+/g, "-");
  const depFor = new Map();
  for (const d of changed) {
    depFor.set(norm(d.name), d);
    if (d.registryName) depFor.set(norm(d.registryName), d);
  }
  const seen = new Set();
  let preexisting = 0;
  for (const v of vulns) {
    const k = `${v.package}\0${v.id}`;
    if (seen.has(k)) continue;
    seen.add(k);
    const name = norm(v.package);
    const changedCaller = (v.callers || []).find((c) => ctx.diff.has(c.file));
    const isNew = depFor.has(name) || Boolean(changedCaller);
    if (!isNew) {
      preexisting++;
      continue;
    }
    // A scanner that publishes no severity (govulncheck, pip-audit) is read as high: a gate
    // cannot shrug off a known, fixable vulnerability for lack of a score.
    const sev = v.severity === "unknown" ? "high" : v.severity;
    const blocks = v.fixed && (sev === "high" || sev === "critical");
    const dep = depFor.get(name);
    res.findings.push({
      rule: v.id || "vulnerability",
      severity: sev,
      file: changedCaller?.file || dep?.file || v.file || "",
      line: changedCaller?.line || dep?.line || 0,
      message: t(v.fixed ? "deps.vuln_fixable" : "deps.vuln_nofix", { pkg: v.package, version: v.version, id: [v.id, ...(v.aliases || [])].join(", "), summary: v.summary, tool: v.tool }),
      blocking: blocks,
    });
  }
  res.metrics.vulnerabilities_total = seen.size;
  res.metrics.preexisting = preexisting;
  if (errors.length) res.metrics.errors = errors;
  return settle(res, {
    failSummary: t("deps.vuln_fail", { count: res.findings.filter((f) => f.blocking).length }),
    passSummary: preexisting ? t("deps.vuln_preexisting", { count: preexisting }) : t("deps.vuln_none"),
  });
}
