// mutation: mutants only on the lines this diff changed (gremlins or go-mutesting, StrykerJS,
// mutmut, cargo-mutants, PIT), under a time budget. A surviving mutant is a warning, as in
// Google's code review; in files that match --critical the check blocks when efficacy
// (killed / (killed + survived)) falls under mutation_min_efficacy (70). Blocking mutants go back
// to test design, since development cannot touch locked tests.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { rawDiff } from "../git.js";
import { t } from "../i18n.js";
import { decodeXml } from "../parsers.js";
import { firstLine, isSourceFile, isTestFile, matchesAny, ranges, relTo, tryJSON } from "../util.js";
import { absDir, exec, fresh, guardPaths, javaBuild, missingTool, newResult, projectFiles, skip, timedOut, use } from "./common.js";

export const id = "mutation";

export function parseGremlins(doc) {
  const map = { KILLED: "killed", LIVED: "survived", "NOT COVERED": "no_coverage", "TIMED OUT": "timeout" };
  const out = [];
  for (const f of doc?.files || []) {
    for (const m of f.mutations || []) out.push({ file: f.file_name, line: m.line, status: map[m.status] || "other", mutator: m.type });
  }
  return out;
}

export function parseStryker(doc) {
  const map = { Killed: "killed", Survived: "survived", NoCoverage: "no_coverage", Timeout: "timeout" };
  const out = [];
  for (const [file, f] of Object.entries(doc?.files || {})) {
    for (const m of f.mutants || []) out.push({ file, line: m.location?.start?.line || 0, status: map[m.status] || "other", mutator: `${m.mutatorName}${m.replacement ? ` → ${m.replacement}` : ""}` });
  }
  return out;
}

export function parsePit(xml) {
  const out = [];
  const map = { KILLED: "killed", SURVIVED: "survived", NO_COVERAGE: "no_coverage", TIMED_OUT: "timeout" };
  for (const m of xml.matchAll(/<mutation\b([^>]*)>([\s\S]*?)<\/mutation>/g)) {
    const status = /status=['"](\w+)['"]/.exec(m[1])?.[1];
    const tag = (n) => decodeXml(new RegExp(`<${n}>([^<]*)</${n}>`).exec(m[2])?.[1] || "");
    const cls = tag("mutatedClass");
    const pkg = cls.includes(".") ? cls.slice(0, cls.lastIndexOf(".")).replace(/\./g, "/") + "/" : "";
    out.push({ file: pkg + tag("sourceFile"), line: Number(tag("lineNumber")), status: map[status] || "other", mutator: tag("description") || tag("mutator") });
  }
  return out;
}

// parseCargoMutantsMissed reads mutants.out/missed.txt: "src/lib.rs:12:5: replace f -> bool with true".
export function parseCargoMutantsMissed(text) {
  const out = [];
  for (const m of (text || "").matchAll(/^(.+?):(\d+):\d+: (.+)$/gm)) out.push({ file: m[1], line: Number(m[2]), status: "survived", mutator: m[3] });
  return out;
}

// parseGoMutesting reads go-mutesting's output: each mutant prints its diff and then
// PASS/FAIL "<tmp>/<file>.go.<n>". FAIL means the tests did not catch it.
export function parseGoMutesting(text) {
  const out = [];
  let block = [];
  for (const line of (text || "").split("\n")) {
    const m = /^(PASS|FAIL|SKIP) "(.+?\.go)\.\d+" with checksum/.exec(line);
    if (!m) {
      block.push(line);
      continue;
    }
    let lineNo = 0;
    const hunk = block.findIndex((l) => /^@@ -\d+/.test(l));
    if (hunk >= 0) {
      let n = Number(/^@@ -(\d+)/.exec(block[hunk])[1]);
      for (const l of block.slice(hunk + 1)) {
        if (l.startsWith("-")) {
          lineNo = n;
          break;
        }
        if (!l.startsWith("+")) n++;
      }
    }
    out.push({ file: m[2], line: lineNo, status: m[1] === "PASS" ? "killed" : m[1] === "FAIL" ? "survived" : "other", mutator: "go-mutesting" });
    block = [];
  }
  return out;
}

function mapFile(ctx, p, f) {
  const rel = relTo(ctx.root, f);
  if (ctx.diff.has(rel)) return rel;
  const inProject = p.dir ? join(p.dir, rel) : rel;
  if (ctx.diff.has(inProject)) return inProject;
  return [...ctx.diff.keys()].find((k) => k.endsWith("/" + rel)) || inProject;
}

export async function run(ctx) {
  const res = newResult(id);
  if (!ctx.flags.mutation) return skip(res, t("mutation.not_requested"));
  const targets = ctx.projects
    .map((p) => ({ p, files: projectFiles(p, (f) => isSourceFile(f) && !isTestFile(f) && ctx.diff.get(f)?.addedCount > 0) }))
    .filter((x) => x.files.length);
  if (!targets.length) return skip(res, t("mutation.nothing_changed"));
  const mutants = [];
  const missing = new Set();
  const failed = [];
  let ran = 0;
  const started = Date.now();
  const budget = ctx.timeoutMs(id);
  for (const { p, files } of targets) {
    const remaining = budget - (Date.now() - started);
    if (remaining <= 0) return timedOut(res, budget / 1000, true);
    const dir = absDir(ctx, p.dir);
    const opts = { cwd: dir, timeoutMs: remaining };
    let got = null;
    if (p.stack === "go") got = await goMutation(ctx, res, p, dir, files, opts);
    else if (p.stack === "ts" || p.stack === "js") got = await stryker(ctx, res, p, dir, files, opts);
    else if (p.stack === "python") got = await mutmut(ctx, res, p, dir, files, opts);
    else if (p.stack === "rust") got = await cargoMutants(ctx, res, p, dir, opts);
    else if (p.stack === "java") got = await pit(ctx, res, p, dir, files, opts);
    if (got?.timedOut) return timedOut(res, budget / 1000, true);
    if (got?.missing) missing.add(got.missing);
    if (got?.failed) failed.push(got.failed);
    if (got?.mutants) {
      ran++;
      for (const m of got.mutants) mutants.push({ ...m, file: mapFile(ctx, p, m.file) });
    }
  }
  if (failed.length) res.metrics.errors = failed;
  // A mutation tool that crashed reads as "not measured", never as "no mutants survived". The
  // check stays advisory, like on a timeout, so it skips instead of failing the run, and it is
  // listed as not verified (a project profile can accept that).
  if (!ran && failed.length) {
    res.metrics.not_verified = true;
    return skip(res, failed.join("; "));
  }
  if (!ran) return missingTool(res, [...missing]);

  // A mutant with no line (mutmut 2) counts only when its file is one the diff changed.
  const onDiff = mutants.filter((m) => ctx.diff.has(m.file) && (!m.line || ctx.diff.get(m.file).added.has(m.line)));
  const count = (list, s) => list.filter((m) => m.status === s).length;
  // Efficacy is Stryker's mutation score: detected (killed or timed out) over every mutant that
  // ran, so a mutant no test reaches counts against it. Leaving those out let a critical file with
  // no test at all pass with no efficacy to compare.
  const score = (list) => {
    const detected = count(list, "killed") + count(list, "timeout");
    const undetected = count(list, "survived") + count(list, "no_coverage");
    return detected + undetected ? Math.round((detected / (detected + undetected)) * 1000) / 10 : null;
  };
  const killed = count(onDiff, "killed");
  const survived = count(onDiff, "survived");
  const efficacy = score(onDiff);
  const critical = onDiff.filter((m) => matchesAny(m.file, ctx.cfg.critical || []));
  const criticalEfficacy = score(critical);
  const criticalBlocks = criticalEfficacy !== null && criticalEfficacy < ctx.cfg.mutation_min_efficacy;
  Object.assign(res.metrics, {
    mutants: onDiff.length,
    killed,
    survived,
    no_coverage: count(onDiff, "no_coverage"),
    timeout: count(onDiff, "timeout"),
    efficacy,
    critical_globs: ctx.cfg.critical || [],
    critical_efficacy: criticalEfficacy,
    threshold: ctx.cfg.mutation_min_efficacy,
  });
  if (missing.size) res.metrics.not_run = [...missing];
  for (const m of onDiff.filter((x) => x.status === "survived" || x.status === "no_coverage")) {
    const isCritical = matchesAny(m.file, ctx.cfg.critical || []);
    res.findings.push({
      rule: m.status === "survived" ? "mutant-survived" : "mutant-not-covered",
      severity: isCritical ? "high" : "medium",
      file: m.file,
      line: m.line,
      message: t(m.status === "survived" ? "mutation.survived" : "mutation.not_covered", { mutator: m.mutator || "?" }),
      blocking: isCritical && criticalBlocks,
    });
  }
  if (criticalBlocks) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("mutation.critical_fail", { efficacy: criticalEfficacy, min: ctx.cfg.mutation_min_efficacy });
  } else if (res.findings.length) {
    res.status = "warn";
    res.summary = t("mutation.survivors", { survived: res.findings.length, total: onDiff.length, efficacy: efficacy ?? "-" });
  } else {
    res.status = "pass";
    res.summary = onDiff.length ? t("mutation.all_killed", { total: onDiff.length }) : t("mutation.no_mutants");
  }
  return res;
}

async function goMutation(ctx, res, p, dir, files, opts) {
  const g = ctx.toolbox.find("gremlins");
  if (g) {
    await use(ctx, res, "gremlins", g);
    const outFile = join(ctx.out, "raw", `gremlins-${p.dir.replace(/\W+/g, "_") || "root"}.json`);
    mkdirSync(join(ctx.out, "raw"), { recursive: true });
    fresh(outFile);
    const r = await exec(ctx, res, g, ["unleash", "--diff", ctx.base, "--output", outFile, "."], opts);
    if (r.timedOut) return { timedOut: true };
    const doc = existsSync(outFile) ? tryJSON(readFileSync(outFile, "utf8")) : undefined;
    if (!doc && r.code !== 0) return { failed: toolFailed("gremlins", r) };
    return { mutants: doc ? parseGremlins(doc) : [] };
  }
  const gm = ctx.toolbox.find("go-mutesting");
  if (!gm) return { missing: "gremlins" };
  await use(ctx, res, "go-mutesting", gm);
  const r = await exec(ctx, res, gm, files.filter((f) => f.endsWith(".go")), opts);
  if (r.timedOut) return { timedOut: true };
  return { mutants: parseGoMutesting(r.stdout) };
}

async function stryker(ctx, res, p, dir, files, opts) {
  if (!p.stryker) return { missing: "stryker" };
  const bin = ctx.toolbox.find("stryker", dir);
  if (!bin) return { missing: "stryker" };
  await use(ctx, res, "stryker", bin);
  const mutate = [];
  for (const f of files) {
    const full = p.dir ? join(p.dir, f) : f;
    for (const [a, b] of ranges(ctx.diff.get(full).added)) mutate.push(`${f}:${a}-${b}`);
  }
  // The json reporter only writes inside the project; the incremental file can go to --out.
  const raw = join(ctx.out, "raw");
  mkdirSync(raw, { recursive: true });
  const tag = p.dir ? p.dir.replace(/[^A-Za-z0-9_.-]+/g, "_") : "root";
  const incremental = join(raw, `stryker-incremental-${tag}.json`);
  const copy = join(raw, `stryker-${tag}.json`);
  fresh(copy);
  const restore = guardPaths(dir, STRYKER_LEFTOVERS);
  try {
    const r = await exec(ctx, res, bin, ["run", "--incremental", "--incrementalFile", incremental, "--mutate", mutate.join(","), "--reporters", "json,clear-text"], opts);
    if (r.timedOut) return { timedOut: true };
    const report = join(dir, "reports", "mutation", "mutation.json");
    const text = existsSync(report) ? readFileSync(report, "utf8") : null;
    if (text !== null) writeFileSync(copy, text);
    const doc = text !== null ? tryJSON(text) : undefined;
    if (!doc && r.code !== 0) return { failed: toolFailed("stryker", r) };
    return { mutants: doc ? parseStryker(doc) : [] };
  } finally {
    restore();
  }
}

// What a Stryker run can leave in the project: its sandbox (kept when a run is killed), the json
// report and the default incremental file.
const STRYKER_LEFTOVERS = [".stryker-tmp", "reports", "reports/mutation", "reports/mutation/mutation.json", "reports/stryker-incremental.json"];

// mutmut keeps its cache (2.x) or its mutated copy (3.x) in the project.
const MUTMUT_LEFTOVERS = [".mutmut-cache", "mutants"];

async function mutmut(ctx, res, p, dir, files, opts) {
  const bin = ctx.toolbox.find("mutmut");
  if (!bin) return { missing: "mutmut" };
  await use(ctx, res, "mutmut", bin);
  const version = await ctx.toolbox.version("mutmut", bin);
  const v2 = /\b2\.\d+/.test(version);
  if (!v2 && !p.mutmutConfig) {
    res.metrics.mutmut = t("mutation.mutmut_config");
    return { missing: "mutmut" };
  }
  const py = files.filter((f) => f.endsWith(".py"));
  const restore = guardPaths(dir, MUTMUT_LEFTOVERS);
  let out;
  let lineOf = () => 0;
  try {
    // mutmut 3 mutates every file under source_paths; the names it takes run only the mutants of
    // the changed modules' functions and methods.
    const args = v2 ? ["run", "--paths-to-mutate", py.join(",")] : ["run", ...py.flatMap((f) => [`${mutmutModule(f)}.x_*`, `${mutmutModule(f)}.x\u01c1*`])];
    const r = await exec(ctx, res, bin, args, opts);
    if (r.timedOut) return { timedOut: true };
    // The changed files define no function mutmut can mutate (only module-level code).
    if (!v2 && r.code !== 0 && /nothing matches/.test(r.stdout + r.stderr)) return { mutants: [] };
    // 3.x lists killed mutants only with --all; without them efficacy reads 0%.
    out = (await exec(ctx, res, bin, v2 ? ["results"] : ["results", "--all", "true"], { cwd: dir, timeoutMs: 60_000 })).stdout;
    if (r.code !== 0 && !out.trim()) return { failed: toolFailed("mutmut", r) };
    if (!v2) lineOf = mutmutLines(dir, py);
  } finally {
    restore();
  }
  return { mutants: v2 ? parseMutmut2(out) : parseMutmut3(out, py, lineOf) };
}

// parseMutmut2 reads `mutmut results` of 2.x: survivors by file, with no line.
export function parseMutmut2(out) {
  const mutants = [];
  let file = null;
  let section = null;
  for (const line of out.split("\n")) {
    const sec = /^(Survived|Timed out|Suspicious|Untested)/.exec(line);
    if (sec) section = sec[1];
    const fm = /^---- (.+?) \(\d+\) ----$/.exec(line);
    if (fm) file = fm[1];
    else if (file && section === "Survived" && /^\d+(,\s*\d+)*$/.test(line.trim())) {
      for (const _ of line.split(",")) mutants.push({ file, line: 0, status: "survived", mutator: "mutmut" });
    }
  }
  return mutants;
}

// mutmutModule is the module name mutmut 3 gives a project file: dots for slashes, without a
// leading src. and without __init__.
export function mutmutModule(file) {
  return file
    .replace(/\.py$/, "")
    .replace(/\//g, ".")
    .replace(/^src\./, "")
    .replace(/\.__init__$/, "");
}

const MUTMUT3_STATUS = { killed: "killed", survived: "survived", "no tests": "no_coverage", timeout: "timeout" };

// parseMutmut3 reads `mutmut results --all true`: "    calc.stats.x_median__mutmut_5: survived",
// methods as "pkg.mod.xǁClassǁmethod__mutmut_2". Mutants of files the diff did not change (mutmut
// generates them for every file, "not checked") are dropped.
export function parseMutmut3(out, files, lineOf = () => 0) {
  const byModule = new Map(files.map((f) => [mutmutModule(f), f]));
  const mutants = [];
  for (const m of out.matchAll(/^\s*(\S+?__mutmut_\d+):\s*(\S.*?)\s*$/gm)) {
    const parts = m[1].split(".");
    const at = parts.findIndex((x) => x.startsWith("x_") || x.startsWith("x\u01c1"));
    const mod = parts.slice(0, at).join(".");
    const file = byModule.get(mod);
    if (!file) continue;
    const name = parts.slice(at).join(".");
    mutants.push({ file, line: lineOf(file, name), status: MUTMUT3_STATUS[m[2]] || "other", mutator: `mutmut ${name}` });
  }
  return mutants;
}

// mutmutLines finds the source line of each mutmut 3 mutant. mutants/<file> holds the original
// function (x_f__mutmut_orig) and every mutated copy, and mutants/<file>.spans the line range of
// each; the first line where a copy differs from the original, counted from the def, is the
// mutated line, at the same offset from the def in the real file. It has to run before the
// mutants/ directory is removed. A mutant it cannot place gets line 0.
export function mutmutLines(dir, files) {
  const byFile = new Map();
  for (const f of files) {
    const spans = tryJSON(readSafe(join(dir, "mutants", `${f}.spans`)))?.spans;
    if (!spans) continue;
    byFile.set(f, { spans, mutated: readSafe(join(dir, "mutants", f)).split("\n"), source: readSafe(join(dir, f)).split("\n") });
  }
  return (file, name) => {
    const d = byFile.get(file);
    const base = name.split("__mutmut_")[0];
    const orig = d?.spans[`${base}__mutmut_orig`];
    const mut = d?.spans[name];
    if (!orig || !mut) return 0;
    const norm = (l) => (l || "").replaceAll(`${base}__mutmut_orig`, "\0").replaceAll(name, "\0").trim();
    const a = d.mutated.slice(orig[0] - 1, orig[1]);
    const b = d.mutated.slice(mut[0] - 1, mut[1]);
    const isDef = (l) => /^\s*(?:async\s+)?def\s/.test(l);
    const da = a.findIndex(isDef);
    const db = b.findIndex(isDef);
    if (da < 0 || db < 0) return 0;
    let k = 0;
    while (da + k < a.length && norm(a[da + k]) === norm(b[db + k])) k++;
    if (da + k >= a.length) return 0;
    const srcDef = defLine(d.source, base);
    if (srcDef < 0 || d.source[srcDef + k]?.trim() !== a[da + k].trim()) return 0;
    return srcDef + k + 1;
  };
}

// defLine is the 0-based line of the def mutmut calls x_f (a function) or xǁClassǁmethod.
function defLine(lines, base) {
  const esc = (x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  if (base.startsWith("x_")) {
    const re = new RegExp(`^(?:async\\s+)?def\\s+${esc(base.slice(2))}\\s*\\(`);
    return lines.findIndex((l) => re.test(l));
  }
  const [, cls, method] = base.split("\u01c1");
  const c = lines.findIndex((l) => new RegExp(`^\\s*class\\s+${esc(cls)}\\b`).test(l));
  if (c < 0) return -1;
  const re = new RegExp(`^\\s+(?:async\\s+)?def\\s+${esc(method)}\\s*\\(`);
  for (let i = c + 1; i < lines.length; i++) if (re.test(lines[i])) return i;
  return -1;
}

function readSafe(p) {
  try {
    return readFileSync(p, "utf8");
  } catch {
    return "";
  }
}

// toolFailed names a mutation tool that exited non-zero without leaving its results.
function toolFailed(tool, r) {
  return t("common.tool_failed", { tool, detail: firstLine(r.stderr || r.stdout) || `exit ${r.code}` });
}

async function cargoMutants(ctx, res, p, dir, opts) {
  if (!ctx.toolbox.find("cargo-mutants")) return { missing: "cargo-mutants" };
  await use(ctx, res, "cargo-mutants", ctx.toolbox.find("cargo-mutants"));
  const raw = join(ctx.out, "raw");
  mkdirSync(raw, { recursive: true });
  const patch = join(raw, "mutants.diff");
  fresh(join(raw, "mutants.out"));
  writeFileSync(patch, await rawDiff(ctx.run, ctx.root, ctx.base));
  const r = await exec(ctx, res, "cargo", ["mutants", "--in-diff", patch, "--output", raw], opts);
  if (r.timedOut) return { timedOut: true };
  const outDir = join(raw, "mutants.out");
  const read = (n) => (existsSync(join(outDir, n)) ? readFileSync(join(outDir, n), "utf8") : "");
  // Exit 2 means missed mutants and 3 timeouts; 4 is a test suite that fails before mutating, and
  // anything else a usage or build error.
  if (r.code !== 0 && r.code !== 2 && r.code !== 3) return { failed: toolFailed("cargo mutants", r) };
  const mutants = parseCargoMutantsMissed(read("missed.txt"));
  for (const m of read("caught.txt").matchAll(/^(.+?):(\d+):\d+: (.+)$/gm)) mutants.push({ file: m[1], line: Number(m[2]), status: "killed", mutator: m[3] });
  for (const m of read("timeout.txt").matchAll(/^(.+?):(\d+):\d+: (.+)$/gm)) mutants.push({ file: m[1], line: Number(m[2]), status: "timeout", mutator: m[3] });
  return { mutants };
}

async function pit(ctx, res, p, dir, files, opts) {
  if (!p.pitest) return { missing: "pitest" };
  const { tool, bin } = javaBuild(ctx, p);
  if (!bin) return { missing: tool };
  await use(ctx, res, `${tool} pitest`, ctx.toolbox.find(tool));
  const classes = files
    .map((f) => /src\/main\/(?:java|kotlin)\/(.+)\.(?:java|kt)$/.exec(f)?.[1])
    .filter(Boolean)
    .map((c) => c.replace(/\//g, "."));
  const report = p.manager === "maven" ? join(dir, "target", "pit-reports", "mutations.xml") : join(dir, "build", "reports", "pitest", "mutations.xml");
  // The report sits in the build directory from one run to the next: a run that fails before
  // writing must not hand back the previous one.
  fresh(report);
  const r =
    p.manager === "maven"
      ? await exec(ctx, res, bin, ["-B", "org.pitest:pitest-maven:mutationCoverage", `-DtargetClasses=${classes.join(",")}`, "-DoutputFormats=XML", "-DtimestampedReports=false"], opts)
      : await exec(ctx, res, bin, ["pitest"], opts);
  if (r.timedOut) return { timedOut: true };
  if (!existsSync(report)) return r.code !== 0 ? { failed: toolFailed(`${tool} pitest`, r) } : { mutants: [] };
  return { mutants: parsePit(readFileSync(report, "utf8")) };
}
