// lint: the repo's own linters and formatters, filtered to lines this diff added,
// plus per-function complexity with lizard. Each tool reads the repo's config
// (.golangci.yml, eslint config, ruff.toml/pyproject, clippy.toml), so the gate follows what the
// team already agreed on. A finding blocks only when the change made it: when a linter or a
// formatter flags something, the same tool runs on the base checkout (cached per base commit)
// and what base already had is reported as pre-existing.

import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { baseLine, showFile } from "../git.js";
import { t } from "../i18n.js";
import { fromSarif } from "../sarif.js";
import { firstLine, isTestFile, langOf, relTo, tryJSON } from "../util.js";
import { absDir, exec, fresh, javaBuild, markPreexisting, missingTool, newResult, notePreexisting, onlyAdded, preexisting, projectFiles, settle, skip, use } from "./common.js";

export const id = "lint";

export async function run(ctx) {
  const res = newResult(id);
  if (!ctx.projects.length) return skip(res, t("lint.no_projects"));
  const missing = new Set();
  let ran = 0;
  for (const p of ctx.projects) {
    const dir = absDir(ctx, p.dir);
    const parts =
      p.stack === "go" ? [goLint, gofmt] : p.stack === "ts" || p.stack === "js" ? [eslint, prettier] : p.stack === "python" ? [ruff] : p.stack === "rust" ? [clippy, cargoFmt] : [javaLint];
    for (const part of parts) {
      const out = await part(ctx, res, p, dir);
      if (out?.missing) missing.add(out.missing);
      if (out?.ran) ran++;
      if (out?.error) res.metrics.errors = [...(res.metrics.errors || []), out.error];
    }
  }
  const cx = await complexity(ctx, res);
  if (cx?.ran) ran++;
  if (!ran && res.metrics.errors?.length) {
    res.status = "error";
    res.summary = res.metrics.errors.join("; ");
    return res;
  }
  if (!ran) return missing.size ? missingTool(res, [...missing]) : skip(res, t("lint.not_configured"));
  if (missing.size) res.metrics.not_run = [...missing];
  settle(res, { passSummary: missing.size ? t("lint.clean_partial", { missing: [...missing].join(", ") }) : t("lint.clean") });
  if (res.metrics.base_not_compared && res.blocking) res.summary = `${res.summary}; ${t("common.base_not_compared")}`;
  notePreexisting(res);
  // A linter that could not run is an error unless something else already blocks: a warning from
  // another linter must not hide it.
  if (res.status !== "fail" && res.metrics.errors?.length) {
    res.status = "error";
    res.summary = res.metrics.errors.join("; ");
  }
  return res;
}

function add(ctx, res, findings) {
  for (const f of onlyAdded(ctx, findings)) res.findings.push(f);
}

// basePath is where a changed file was at base (its old name after a rename), relative to the
// project directory; null for a file base did not have.
function basePath(ctx, p, file) {
  const d = ctx.diff.get(file);
  if (d && d.status === "A") return null;
  const old = d?.oldPath || file;
  return p.dir ? (old.startsWith(p.dir + "/") ? old.slice(p.dir.length + 1) : null) : old;
}

// atBase answers, per changed file, what tool said about it on the base checkout: findings for a
// linter, {unformatted} for a formatter. Each file is cached per base commit, tool and version, so
// a second run on the same base only asks about files it has not seen. runBase(baseDir, rels)
// returns Map<rel, value> or null when the tool could not run there. The result maps each repo
// path to its value (null: not at base); null when base could not be checked out or run.
async function atBase(ctx, res, p, tool, files, runBase) {
  const version = (res._tools.find((x) => x.name === tool) || {}).version || "";
  const keyOf = (rel) => ["lint", tool, version, p.dir, rel];
  const out = new Map();
  const todo = [];
  for (const f of files) {
    const rel = basePath(ctx, p, f);
    if (rel === null) {
      out.set(f, null);
      continue;
    }
    const hit = ctx.cache.get(keyOf(rel));
    if (hit !== undefined) out.set(f, hit);
    else todo.push({ f, rel });
  }
  if (!todo.length) return out;
  const baseDir = await ctx.baseProjectDir(p);
  if (!baseDir) {
    res.metrics.base_not_compared = true;
    return null;
  }
  const present = todo.filter((x) => existsSync(join(baseDir, x.rel)));
  const got = present.length ? await runBase(baseDir, present.map((x) => x.rel)) : new Map();
  if (!got) {
    res.metrics.base_not_compared = true;
    return null;
  }
  for (const x of todo) {
    const value = present.includes(x) ? (got.get(x.rel) ?? null) : null;
    if (present.includes(x)) ctx.cache.set(keyOf(x.rel), value);
    out.set(x.f, value);
  }
  return out;
}

// compareLines marks as pre-existing the findings on added lines that base already had in the same
// file, with the same rule and message (counted, so a second copy of an old finding is new). all
// holds the tool's findings over the whole of each changed file; only the ones on added lines are
// kept. It asks base only when one of those blocks.
async function compareLines(ctx, res, p, tool, all, runBase) {
  const keep = onlyAdded(ctx, all);
  if (keep.some((f) => f.blocking)) {
    const files = [...new Set(keep.filter((f) => f.blocking).map((f) => f.file))];
    const base = await atBase(ctx, res, p, tool, files, runBase);
    if (base) {
      const before = files.flatMap((f) => (base.get(f) || []).map((b) => ({ ...b, file: f })));
      markPreexisting(
        ctx,
        all.filter((f) => files.includes(f.file)),
        before,
        (f) => `${f.file}\0${f.rule}\0${f.message}`,
      );
    }
  }
  for (const f of keep) res.findings.push(f);
}

// compareFormat turns the files a formatter would rewrite into findings: blocking when the file
// was formatted at base (or is new), pre-existing when base would have been rewritten too.
async function compareFormat(ctx, res, p, tool, rule, files, runBase) {
  if (!files.length) return;
  const base = await atBase(ctx, res, p, tool, files, runBase);
  for (const file of files) {
    const f = { rule, severity: "high", file, line: 0, message: t("lint.not_formatted", { tool }), blocking: true };
    if (base?.get(file)?.unformatted) {
      preexisting(f);
      f.message = t("lint.not_formatted_base", { tool });
    }
    res.findings.push(f);
  }
}

async function goLint(ctx, res, p, dir) {
  const bin = ctx.toolbox.find("golangci-lint");
  if (bin) {
    await use(ctx, res, "golangci-lint", bin);
    const version = await ctx.toolbox.version("golangci-lint", bin);
    const v2 = /version v?2\./.test(version) || /\bv?2\.\d+\.\d+/.test(version);
    const sarifPath = join(ctx.out, "raw", `golangci-lint-${p.dir.replace(/\W+/g, "_") || "root"}.sarif`);
    mkdirSync(dirname(sarifPath), { recursive: true });
    fresh(sarifPath);
    // golangci-lint compares with base itself: it reports only issues the diff introduced.
    const args = v2
      ? ["run", `--new-from-merge-base=${ctx.base}`, `--output.sarif.path=${sarifPath}`, "--output.text.path=stderr", "./..."]
      : ["run", `--new-from-rev=${ctx.base}`, "--out-format=sarif", "./..."];
    const r = await exec(ctx, res, bin, args, { cwd: dir });
    if (r.timedOut) return { error: t("common.timeout", { seconds: ctx.timeoutMs(id) / 1000 }) };
    const text = v2 ? (existsSync(sarifPath) ? readFileSync(sarifPath, "utf8") : "") : r.stdout;
    const doc = tryJSON(text);
    if (!doc) {
      if (r.code !== 0) return { error: `golangci-lint: ${firstLine(r.stderr || r.stdout)}` };
      return { ran: true };
    }
    if (!v2) writeFileSync(sarifPath, text);
    add(
      ctx,
      res,
      fromSarif(doc, ctx.root).map((f) => ({ ...f, file: fixGoPath(ctx, p, f.file), severity: f.severity === "low" || f.severity === "info" ? "medium" : "high", blocking: f.severity !== "low" && f.severity !== "info" })),
    );
    return { ran: true };
  }
  const go = ctx.toolbox.find("go");
  if (!go) return { missing: "golangci-lint" };
  await use(ctx, res, "go vet", go);
  const vet = async (cwd) => (await exec(ctx, res, "go", ["vet", "./..."], { cwd })).stderr;
  const parse = (text, file) => [...text.matchAll(/^(?:vet: )?(\.?\/?[^\s:]+\.go):(\d+):(?:\d+:)? (.+)$/gm)].map((m) => ({ rule: "govet", severity: "high", file: file(m[1]), line: Number(m[2]), message: m[3], blocking: true }));
  const all = parse(await vet(dir), (f) => fixGoPath(ctx, p, f));
  await compareLines(ctx, res, p, "go vet", all, async (baseDir, rels) => {
    const found = parse(await vet(baseDir), (f) => f.replace(/^\.\//, ""));
    return new Map(rels.map((rel) => [rel, found.filter((x) => x.file === rel).map(({ rule, message }) => ({ rule, message }))]));
  });
  return { ran: true, missing: "golangci-lint" };
}

function fixGoPath(ctx, p, f) {
  const rel = relTo(ctx.root, f);
  if (ctx.diff.has(rel) || !p.dir) return rel;
  const joined = join(p.dir, rel.replace(/^\.\//, ""));
  return joined;
}

// listed runs a formatter that prints the files it would rewrite, one per line.
async function listed(ctx, res, bin, args, files, cwd) {
  const r = await exec(ctx, res, bin, [...args, ...files], { cwd });
  return r.stdout.split("\n").filter(Boolean);
}

async function gofmt(ctx, res, p, dir) {
  const files = projectFiles(p, (f) => f.endsWith(".go"));
  if (!files.length) return {};
  const bin = ctx.toolbox.find("gofmt");
  if (!bin) return { missing: "gofmt" };
  await use(ctx, res, "gofmt", bin);
  const bad = (await listed(ctx, res, bin, ["-l"], files, dir)).map((f) => (p.dir ? join(p.dir, f) : f));
  await compareFormat(ctx, res, p, "gofmt", "gofmt", bad, async (baseDir, rels) => {
    const still = new Set(await listed(ctx, res, bin, ["-l"], rels, baseDir));
    return new Map(rels.map((rel) => [rel, { unformatted: still.has(rel) }]));
  });
  return { ran: true };
}

const JS_EXT = /\.(m|c)?(j|t)sx?$/;

async function eslintRun(ctx, res, bin, cwd, files, legacy) {
  const env = legacy && !process.env.ESLINT_USE_FLAT_CONFIG ? { ESLINT_USE_FLAT_CONFIG: "false" } : undefined;
  const r = await exec(ctx, res, bin, ["--format", "json", "--no-error-on-unmatched-pattern", ...files], { cwd, env });
  const doc = tryJSON(r.stdout);
  if (!Array.isArray(doc)) return { error: `eslint: ${firstLine(r.stderr || r.stdout)}` };
  const findings = [];
  for (const file of doc) {
    for (const m of file.messages || []) {
      findings.push({ rule: m.ruleId || "eslint", severity: m.severity === 2 ? "high" : "medium", file: relTo(cwd, file.filePath), line: m.line || 0, message: m.message, blocking: m.severity === 2 });
    }
  }
  return { findings };
}

async function eslint(ctx, res, p, dir) {
  const files = projectFiles(p, (f) => JS_EXT.test(f));
  if (!files.length || !p.eslintConfig) return {};
  const bin = ctx.toolbox.find("eslint", dir);
  if (!bin) return { missing: "eslint" };
  await use(ctx, res, "eslint", bin);
  const head = await eslintRun(ctx, res, bin, dir, files, p.eslintLegacy);
  if (head.error) return { error: head.error };
  const all = head.findings.map((f) => ({ ...f, file: p.dir ? join(p.dir, f.file) : f.file }));
  await compareLines(ctx, res, p, "eslint", all, async (baseDir, rels) => {
    const base = await eslintRun(ctx, res, bin.startsWith(dir) ? bin.replace(dir, baseDir) : bin, baseDir, rels, p.eslintLegacy);
    if (base.error) return null;
    return new Map(rels.map((rel) => [rel, base.findings.filter((x) => x.file === rel).map(({ rule, message }) => ({ rule, message }))]));
  });
  return { ran: true };
}

async function prettier(ctx, res, p, dir) {
  const files = projectFiles(p, (f) => JS_EXT.test(f) || /\.(json|css|scss|md|astro|vue|svelte)$/.test(f));
  if (!files.length || !p.prettierConfig) return {};
  const bin = ctx.toolbox.find("prettier", dir);
  if (!bin) return { missing: "prettier" };
  await use(ctx, res, "prettier", bin);
  const args = ["--list-different", "--ignore-unknown"];
  const bad = (await listed(ctx, res, bin, args, files, dir)).map((f) => (p.dir ? join(p.dir, f) : f));
  await compareFormat(ctx, res, p, "prettier", "prettier", bad, async (baseDir, rels) => {
    const still = new Set(await listed(ctx, res, bin.startsWith(dir) ? bin.replace(dir, baseDir) : bin, args, rels, baseDir));
    return new Map(rels.map((rel) => [rel, { unformatted: still.has(rel) }]));
  });
  return { ran: true };
}

async function ruffCheck(ctx, res, bin, cwd, files) {
  const r = await exec(ctx, res, bin, ["check", "--output-format=json", "--no-fix", ...files], { cwd });
  const doc = tryJSON(r.stdout);
  if (!Array.isArray(doc)) return { error: `ruff: ${firstLine(r.stderr || r.stdout)}` };
  return { findings: doc.map((v) => ({ rule: v.code || "ruff", severity: "high", file: relTo(cwd, v.filename), line: v.location?.row || 0, message: v.message, blocking: true })) };
}

async function ruff(ctx, res, p, dir) {
  const files = projectFiles(p, (f) => f.endsWith(".py"));
  if (!files.length) return {};
  const bin = ctx.toolbox.find("ruff");
  if (!bin) return { missing: "ruff" };
  await use(ctx, res, "ruff", bin);
  const head = await ruffCheck(ctx, res, bin, dir, files);
  if (head.error) return { error: head.error };
  const all = head.findings.map((f) => ({ ...f, file: p.dir ? join(p.dir, f.file) : f.file }));
  await compareLines(ctx, res, p, "ruff", all, async (baseDir, rels) => {
    const base = await ruffCheck(ctx, res, bin, baseDir, rels);
    if (base.error) return null;
    return new Map(rels.map((rel) => [rel, base.findings.filter((x) => x.file === rel).map(({ rule, message }) => ({ rule, message }))]));
  });
  const pyproject = existsSync(join(dir, "pyproject.toml")) ? readFileSync(join(dir, "pyproject.toml"), "utf8") : "";
  const ruffToml = ["ruff.toml", ".ruff.toml"].map((n) => (existsSync(join(dir, n)) ? readFileSync(join(dir, n), "utf8") : "")).join("\n");
  if (/^\[tool\.ruff\.format\]/m.test(pyproject) || /^\[format\]/m.test(ruffToml)) {
    const wouldReformat = async (cwd, list) => [...(await exec(ctx, res, bin, ["format", "--check", ...list], { cwd })).stdout.matchAll(/^Would reformat: (.+)$/gm)].map((m) => m[1]);
    const bad = (await wouldReformat(dir, files)).map((f) => (p.dir ? join(p.dir, f) : f));
    await compareFormat(ctx, res, p, "ruff format", "ruff-format", bad, async (baseDir, rels) => {
      const still = new Set(await wouldReformat(baseDir, rels));
      return new Map(rels.map((rel) => [rel, { unformatted: still.has(rel) }]));
    });
  }
  return { ran: true };
}

async function clippy(ctx, res, p, dir) {
  const cargo = ctx.toolbox.find("cargo");
  if (!cargo) return { missing: "cargo" };
  await use(ctx, res, "cargo clippy", cargo);
  const runClippy = (cwd) => exec(ctx, res, "cargo", ["clippy", "--message-format=json", "--all-targets", "--quiet"], { cwd });
  const head = parseClippy(await runClippy(dir));
  if (head.error) return { error: head.error };
  const all = head.findings.map((f) => ({ ...f, file: p.dir ? join(p.dir, f.file) : f.file }));
  await compareLines(ctx, res, p, "cargo clippy", all, async (baseDir, rels) => {
    const base = parseClippy(await runClippy(baseDir));
    if (base.error) return null;
    return new Map(rels.map((rel) => [rel, base.findings.filter((x) => x.file === rel).map(({ rule, message }) => ({ rule, message }))]));
  });
  return { ran: true };
}

// parseClippy reads cargo's JSON messages. With --all-targets cargo builds a library twice (the
// lib and its test harness) and prints each diagnostic once per target, so they are kept once.
// Paths are relative to the crate.
function parseClippy(r) {
  const findings = [];
  const seen = new Set();
  for (const line of r.stdout.split("\n")) {
    const ev = tryJSON(line);
    if (ev?.reason !== "compiler-message") continue;
    const m = ev.message;
    if (m.level !== "warning" && m.level !== "error") continue;
    const span = (m.spans || []).find((s) => s.is_primary) || m.spans?.[0];
    if (!span) continue;
    const key = `${m.code?.code}\0${span.file_name}\0${span.line_start}\0${span.column_start}\0${m.message}`;
    if (seen.has(key)) continue;
    seen.add(key);
    findings.push({ rule: m.code?.code || "clippy", severity: m.level === "error" ? "high" : "medium", file: span.file_name, line: span.line_start, message: m.message, blocking: m.level === "error" });
  }
  // cargo exits 101 when clippy is not installed ("no such command") or the build breaks; with no
  // error diagnostic to show for it, that is a tool failure, not a clean lint.
  if (r.code !== 0 && !findings.some((f) => f.blocking)) return { error: `cargo clippy: ${firstLine(r.stderr) || `exit ${r.code}`}` };
  return { findings };
}

async function cargoFmt(ctx, res, p, dir) {
  if (!projectFiles(p, (f) => f.endsWith(".rs")).length || !ctx.toolbox.find("cargo")) return {};
  // 1 lists unformatted files; anything else (101: rustfmt not installed) is a failure.
  const unformatted = async (cwd) => {
    const r = await exec(ctx, res, "cargo", ["fmt", "--check", "--", "-l"], { cwd });
    if (r.code !== 0 && r.code !== 1) return { error: `cargo fmt: ${firstLine(r.stderr) || `exit ${r.code}`}` };
    return { files: r.stdout.split("\n").filter((l) => l.endsWith(".rs")).map((f) => relTo(cwd, f.startsWith("/") ? f : join(cwd, f))) };
  };
  const head = await unformatted(dir);
  if (head.error) return { error: head.error };
  const bad = head.files.map((f) => (p.dir ? join(p.dir, f) : f)).filter((f) => ctx.diff.has(f));
  await compareFormat(ctx, res, p, "rustfmt", "rustfmt", bad, async (baseDir, rels) => {
    const base = await unformatted(baseDir);
    if (base.error) return null;
    return new Map(rels.map((rel) => [rel, { unformatted: base.files.includes(rel) }]));
  });
  return { ran: true };
}

async function javaLint(ctx, res, p, dir) {
  if (!p.checkstyle) return {};
  const { tool, bin } = javaBuild(ctx, p);
  if (!bin) return { missing: tool };
  await use(ctx, res, `${tool} checkstyle`, ctx.toolbox.find(tool));
  const r = await exec(ctx, res, bin, p.manager === "maven" ? ["-B", "checkstyle:check"] : ["checkstyleMain"], { cwd: dir });
  const found = parseCheckstyle(r.stdout + "\n" + r.stderr, r.code !== 0);
  // A failed goal with no violation to show (the plugin did not resolve, the config did not
  // load) is a tool failure, not a clean lint.
  if (r.code !== 0 && !found.length) {
    const why = (r.stdout + "\n" + r.stderr).split("\n").find((l) => /^\[ERROR\]/.test(l)) || r.stderr;
    return { error: `${tool} checkstyle: ${firstLine(why) || `exit ${r.code}`}` };
  }
  add(
    ctx,
    res,
    found.map((f) => ({ ...f, file: relTo(ctx.root, f.file.startsWith("/") ? f.file : join(dir, f.file)) })),
  );
  return { ran: true };
}

// parseCheckstyle reads two shapes of the same violation. maven-checkstyle-plugin lists the ones
// that count against the build as "[WARNING] src/A.java:[39,9] (indentation) Indentation: msg";
// with consoleOutput, or under Gradle, Checkstyle itself prints "[WARN] /abs/A.java:39:9: msg
// [Indentation]". When the plugin's list is there it wins and the console lines, the same
// violations again, are dropped. A violation in that list blocks when the goal failed: the repo
// set violationSeverity, and the build said these break it. Otherwise only Checkstyle errors block.
export function parseCheckstyle(text, failed) {
  const plugin = [];
  for (const m of text.matchAll(/^\[(ERROR|WARNING|INFO)\]\s+(\S+\.(?:java|kt)):\[(\d+)(?:,\d+)?\]\s+\(([\w.-]+)\)\s+(\w+):\s*(.+)$/gm)) {
    plugin.push({ rule: m[5], severity: failed || m[1] === "ERROR" ? "high" : "medium", file: m[2], line: Number(m[3]), message: m[6].trim(), blocking: failed || m[1] === "ERROR" });
  }
  if (plugin.length) return plugin;
  const out = [];
  for (const m of text.matchAll(/\[(ERROR|WARN)\]\s+(\S+\.(?:java|kt)):(\d+)(?::\d+)?:\s*(.+?)(?:\s+\[(\w+)\])?\s*$/gm)) {
    const err = m[1] === "ERROR";
    out.push({ rule: m[5] || "checkstyle", severity: err ? "high" : "medium", file: m[2], line: Number(m[3]), message: m[4], blocking: err });
  }
  return out;
}

// parseLizardCsv reads `lizard --csv`: NLOC,CCN,token,PARAM,length,location,file,function,
// long_name,start,end.
export function parseLizardCsv(text) {
  const out = [];
  for (const line of text.split("\n")) {
    const cols = [];
    let cur = "";
    let q = false;
    for (const ch of line) {
      if (ch === '"') q = !q;
      else if (ch === "," && !q) {
        cols.push(cur);
        cur = "";
      } else cur += ch;
    }
    cols.push(cur);
    if (cols.length < 11 || !/^\d+$/.test(cols[1])) continue;
    out.push({ ccn: Number(cols[1]), file: cols[6], name: cols[7], longName: cols[8], start: Number(cols[9]), end: Number(cols[10]) });
  }
  return out;
}


// lizard runs lizard over files (relative to cwd) and returns its functions.
async function lizard(ctx, res, bin, files, cwd) {
  return parseLizardCsv((await exec(ctx, res, bin, ["--csv", ...files], { cwd })).stdout).map((fn) => ({ ...fn, file: relTo(cwd, fn.file) }));
}

// pairFunctions finds, for each function lizard saw at head, the same function at base: same long
// name (signature) or, failing that, same short name, and among several candidates (lizard names
// every arrow function "(anonymous)") the one whose start line at base is closest to where the
// head function's start maps through the diff.
export function pairFunctions(headFns, baseFns, d) {
  const used = new Set();
  const out = new Map();
  for (const fn of headFns) {
    const at = baseLine(d, fn.start);
    let pool = baseFns.filter((b) => !used.has(b) && b.longName === fn.longName);
    if (!pool.length) pool = baseFns.filter((b) => !used.has(b) && b.name === fn.name);
    if (!pool.length) continue;
    const best = at === null ? pool[0] : pool.reduce((a, b) => (Math.abs(b.start - at) < Math.abs(a.start - at) ? b : a));
    used.add(best);
    out.set(fn, best);
  }
  return out;
}

// complexity runs lizard on the changed files at head and at base (base per file, cached per base
// commit). A function this diff touches blocks when it is new and over the limit, or when it
// crosses the limit; +5 or more warns. One that was over the limit at base and did not grow by 5
// is reported as pre-existing, and one whose complexity went down never counts.
async function complexity(ctx, res) {
  const bin = ctx.toolbox.find("lizard");
  const files = [...ctx.diff.keys()].filter((f) => langOf(f) && !isTestFile(f) && ctx.diff.get(f).addedCount);
  if (!bin || !files.length) return {};
  await use(ctx, res, "lizard", bin);
  const version = await ctx.toolbox.version("lizard", bin);
  const max = ctx.cfg.complexity_max;
  const headFns = await lizard(ctx, res, bin, files, ctx.root);
  const baseByFile = new Map();
  const todo = [];
  for (const f of files) {
    const d = ctx.diff.get(f);
    if (d.status === "A") continue;
    const old = d.oldPath || f;
    const hit = ctx.cache.get(["lizard", version, old]);
    if (hit !== undefined) baseByFile.set(f, hit);
    else todo.push({ f, old });
  }
  if (todo.length) {
    const baseDir = mkdtempSync(join(tmpdir(), "thulu-gates-lizard-"));
    try {
      const present = [];
      for (const x of todo) {
        const text = await showFile(ctx.run, ctx.root, ctx.base, x.old);
        if (text === null) continue;
        mkdirSync(dirname(join(baseDir, x.old)), { recursive: true });
        writeFileSync(join(baseDir, x.old), text);
        present.push(x);
      }
      const fns = present.length ? await lizard(ctx, res, bin, present.map((x) => x.old), baseDir) : [];
      for (const x of present) {
        const mine = fns.filter((fn) => fn.file === x.old).map(({ name, longName, ccn, start, end }) => ({ name, longName, ccn, start, end }));
        ctx.cache.set(["lizard", version, x.old], mine);
        baseByFile.set(x.f, mine);
      }
    } finally {
      rmSync(baseDir, { recursive: true, force: true });
    }
  }
  for (const file of new Set(headFns.map((fn) => fn.file))) {
    const d = ctx.diff.get(file);
    if (!d) continue;
    const mine = headFns.filter((fn) => fn.file === file);
    const pairs = pairFunctions(mine, baseByFile.get(file) || [], d);
    for (const fn of mine) {
      let touched = false;
      for (let l = fn.start; l <= fn.end && !touched; l++) touched = d.added.has(l);
      if (!touched) continue;
      const old = pairs.get(fn)?.ccn;
      if (old !== undefined && fn.ccn <= old) {
        if (fn.ccn > max) res.findings.push(preexisting({ rule: "complexity", severity: "medium", file, line: fn.start, message: t("lint.complexity_base", { fn: fn.name, old, ccn: fn.ccn, max }) }));
        continue;
      }
      const crosses = fn.ccn > max && (old === undefined || old <= max);
      const grew = old !== undefined && fn.ccn - old >= 5;
      if (!crosses && !grew) {
        if (fn.ccn > max) res.findings.push(preexisting({ rule: "complexity", severity: "medium", file, line: fn.start, message: t("lint.complexity_base", { fn: fn.name, old, ccn: fn.ccn, max }) }));
        continue;
      }
      res.findings.push({
        rule: "complexity",
        severity: crosses ? "high" : "medium",
        file,
        line: fn.start,
        message: old === undefined ? t("lint.complexity_new", { fn: fn.name, ccn: fn.ccn, max }) : t("lint.complexity_grew", { fn: fn.name, old, ccn: fn.ccn, max }),
        blocking: crosses,
      });
    }
  }
  return { ran: true };
}
