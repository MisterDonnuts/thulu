// coverage_new: share of new executable lines the tests ran, crossing the diff with the coverage
// reports the tests check left. Blocks under coverage_min (80) once there are at least
// coverage_min_lines (20) new executable lines; warns under coverage_warn (90).

import { existsSync, readFileSync } from "node:fs";
import { isAbsolute, join, normalize } from "node:path";

import { t } from "../i18n.js";
import { coverageFormat, parseCobertura, parseGoCover, parseJacoco, parseLcov } from "../parsers.js";
import { formatRanges, isSourceFile, isTestFile, ranges, relTo } from "../util.js";
import { newResult, skip } from "./common.js";

export const id = "coverage_new";

// loadCoverage merges every report into repo path -> line -> hits.
export function loadCoverage(ctx, entries) {
  const merged = new Map();
  const changed = [...ctx.diff.keys()];
  const resolve = (candidates, raw) => {
    for (const c of candidates) {
      const rel = normalize(c).replace(/^\.\//, "");
      if (ctx.diff.has(rel) || existsSync(join(ctx.root, rel))) return rel;
    }
    const tail = raw.replace(/^\.?\//, "");
    return changed.find((p) => p === tail || p.endsWith("/" + tail)) || null;
  };
  for (const e of entries) {
    if (!existsSync(e.path)) continue;
    const text = readFileSync(e.path, "utf8");
    const fmt = coverageFormat(e.path, text);
    const pdir = e.project?.dir || "";
    const pabs = pdir ? join(ctx.root, pdir) : ctx.root;
    let pairs = [];
    if (fmt === "lcov") {
      for (const [f, lines] of parseLcov(text)) pairs.push([resolve([relTo(ctx.root, isAbsolute(f) ? f : join(pabs, f))], f), lines]);
    } else if (fmt === "gocover") {
      const mod = e.project?.module;
      for (const [f, lines] of parseGoCover(text)) {
        const cands = mod && f.startsWith(mod + "/") ? [join(pdir, f.slice(mod.length + 1))] : [];
        pairs.push([resolve(cands, f), lines]);
      }
    } else if (fmt === "cobertura") {
      const { map, sources } = parseCobertura(text);
      for (const [f, lines] of map) {
        const cands = [...sources.map((s) => relTo(ctx.root, isAbsolute(s) ? join(s, f) : join(pabs, s, f))), join(pdir, f), f];
        pairs.push([resolve(cands, f), lines]);
      }
    } else if (fmt === "jacoco") {
      for (const [f, lines] of parseJacoco(text)) pairs.push([resolve([], f), lines]);
    }
    for (const [file, lines] of pairs) {
      if (!file) continue;
      let m = merged.get(file);
      if (!m) merged.set(file, (m = new Map()));
      for (const [n, hits] of lines) m.set(n, Math.max(m.get(n) || 0, hits));
    }
  }
  return merged;
}

export async function run(ctx) {
  const res = newResult(id);
  res.tool = "thulu-gates/coverage";
  const tests = ctx.results.get("tests");
  // A tests run that was cut or stopped short leaves coverage of the part that ran: reading it
  // would certify lines the rest of the suite might have covered or not.
  if (tests && (tests.status === "error" || tests.metrics?.not_verified)) {
    skip(res, t("coverage.tests_not_finished"));
    res.metrics.not_verified = true;
    return res;
  }
  const entries = ctx.coverage.length ? ctx.coverage : (ctx.cfg.coverage_files || []).map((f) => ({ path: join(ctx.root, f), project: null }));
  if (!entries.length) {
    skip(res, tests && tests.status !== "skip" ? t("coverage.no_report") : t("coverage.no_tests_run"));
    if (!tests || tests.status === "skip") res.metrics.not_verified = true;
    return res;
  }
  const cov = loadCoverage(ctx, entries);
  res.metrics.reports = entries.map((e) => relTo(ctx.root, e.path));
  let newLines = 0;
  let covered = 0;
  const uncovered = [];
  const withoutData = [];
  for (const [file, d] of ctx.diff) {
    if (!isSourceFile(file) || isTestFile(file)) continue;
    const lines = cov.get(file);
    if (!lines) {
      // Only files of a project the tests ran for; a shell script or a stack with no runner has
      // no report to be missing from.
      if (d.addedCount && (!ctx.projects.length || ctx.projects.some((p) => p.files.includes(file)))) withoutData.push(file);
      continue;
    }
    const miss = [];
    for (const n of d.added) {
      if (!lines.has(n)) continue;
      newLines++;
      if (lines.get(n) > 0) covered++;
      else miss.push(n);
    }
    if (miss.length) uncovered.push({ file, ranges: ranges(miss) });
  }
  const pct = newLines ? Math.round((covered / newLines) * 1000) / 10 : 100;
  Object.assign(res.metrics, {
    new_lines: newLines,
    covered_lines: covered,
    percent: pct,
    threshold: ctx.cfg.coverage_min,
    min_lines: ctx.cfg.coverage_min_lines,
    files_without_data: withoutData,
  });
  if (!newLines) {
    res.status = withoutData.length ? "warn" : "pass";
    res.summary = withoutData.length ? t("coverage.no_data_for", { files: withoutData.slice(0, 5).join(", ") }) : t("coverage.no_new_lines");
    return res;
  }
  const gated = newLines >= ctx.cfg.coverage_min_lines;
  const below = pct < ctx.cfg.coverage_min;
  if (gated && below) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("coverage.below", { pct, covered, total: newLines, min: ctx.cfg.coverage_min });
  } else if (below) {
    res.status = "warn";
    res.summary = t("coverage.below_ungated", { pct, covered, total: newLines, lines: ctx.cfg.coverage_min_lines });
  } else if (pct < ctx.cfg.coverage_warn) {
    res.status = "warn";
    res.summary = t("coverage.under_warn", { pct, covered, total: newLines, warn: ctx.cfg.coverage_warn });
  } else {
    res.status = "pass";
    res.summary = t("coverage.ok", { pct, covered, total: newLines });
  }
  // A file no report mentions (a new module no test imports, under a runner that only reports
  // loaded files) stays out of the percentage, which cannot tell its lines from comments; it is
  // listed so the reader sees it, and a pass turns into a warning.
  for (const file of withoutData) {
    res.findings.push({ rule: "no-coverage-data", severity: "low", file, line: 0, message: t("coverage.file_no_data"), blocking: false });
  }
  if (withoutData.length && res.status === "pass") {
    res.status = "warn";
    res.summary = `${res.summary}; ${t("coverage.no_data_for", { files: withoutData.slice(0, 5).join(", ") })}`;
  }
  for (const u of uncovered) {
    for (const [a, b] of u.ranges) {
      res.findings.push({
        rule: "uncovered-new-lines",
        severity: res.blocking ? "medium" : "low",
        file: u.file,
        line: a,
        message: t("coverage.uncovered", { lines: formatRanges([[a, b]]) }),
        blocking: res.blocking,
      });
    }
  }
  return res;
}
