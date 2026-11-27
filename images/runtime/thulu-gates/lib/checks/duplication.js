// duplication: share of new lines that sit inside a clone of at least dup_min_tokens (50) tokens,
// against the whole repo. jscpd when installed; otherwise a built-in detector over exact token
// windows. Blocks above dup_max (3%) with at least coverage_min_lines new lines; under that bar
// every clone, in the same file or across files, is a warning. A clone the change only edited
// (the block was already a copy at base) does not count: when there are clones, each changed
// file's duplicated lines are counted at head and at base (cached per base commit), and only the
// growth counts as new duplication; the rest is reported as pre-existing.

import { existsSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { lsFiles } from "../git.js";
import { t } from "../i18n.js";
import { firstLine, isSourceFile, isTestFile, langOf, relTo, tryJSON } from "../util.js";
import { exec, fresh, newResult, notePreexisting, preexisting, use } from "./common.js";

export const id = "duplication";

const SKIP_DIRS = /(^|\/)(node_modules|vendor|dist|build|target|\.git|testdata|fixtures|third_party|__generated__)\//;
const MAX_FILE_BYTES = 512 * 1024;
const MAX_FILES = 20000;
const HASH_COMMENT = new Set(["python", "ruby", "shell"]);

// tokenize drops whitespace and comments and keeps one token per identifier, number, string or
// punctuation mark, each with its line.
export function tokenize(text, lang) {
  const toks = [];
  let line = 1;
  let i = 0;
  const n = text.length;
  const hashComments = HASH_COMMENT.has(lang);
  while (i < n) {
    const c = text[i];
    if (c === "\n") {
      line++;
      i++;
      continue;
    }
    if (c === " " || c === "\t" || c === "\r") {
      i++;
      continue;
    }
    if ((c === "/" && text[i + 1] === "/" && !hashComments) || (c === "#" && hashComments)) {
      while (i < n && text[i] !== "\n") i++;
      continue;
    }
    if (c === "/" && text[i + 1] === "*" && !hashComments) {
      i += 2;
      while (i < n && !(text[i] === "*" && text[i + 1] === "/")) {
        if (text[i] === "\n") line++;
        i++;
      }
      i += 2;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      const start = i;
      const startLine = line;
      i++;
      while (i < n && text[i] !== c) {
        if (text[i] === "\\") i++;
        else if (text[i] === "\n") {
          if (c !== "`" && !(lang === "python")) break;
          line++;
        }
        i++;
      }
      i++;
      toks.push({ v: text.slice(start, Math.min(i, start + 200)), line: startLine });
      continue;
    }
    const m = /[A-Za-z_$][\w$]*|\d[\w.]*/y;
    m.lastIndex = i;
    const w = m.exec(text);
    if (w) {
      toks.push({ v: w[0], line });
      i += w[0].length;
      continue;
    }
    toks.push({ v: c, line });
    i++;
  }
  return toks;
}

// windowHashes returns a 64-bit rolling hash (two 32-bit halves, as a string key) for every
// window of w tokens.
function windowHashes(ids, w) {
  const out = [];
  if (ids.length < w) return out;
  const B1 = 1000003;
  const B2 = 916_027;
  let p1 = 1;
  let p2 = 1;
  for (let k = 0; k < w - 1; k++) {
    p1 = Math.imul(p1, B1) >>> 0;
    p2 = Math.imul(p2, B2) >>> 0;
  }
  let h1 = 0;
  let h2 = 0;
  for (let k = 0; k < ids.length; k++) {
    if (k >= w) {
      h1 = (h1 - Math.imul(ids[k - w], p1)) >>> 0;
      h2 = (h2 - Math.imul(ids[k - w] ^ 0x5bd1e995, p2)) >>> 0;
    }
    h1 = (Math.imul(h1, B1) + ids[k]) >>> 0;
    h2 = (Math.imul(h2, B2) + (ids[k] ^ 0x5bd1e995)) >>> 0;
    if (k >= w - 1) out.push(`${h1.toString(36)}.${h2.toString(36)}`);
  }
  return out;
}

// detectClones is the built-in detector. changed: [{file, text, added:Set}], corpus: [{file,
// text}] (may include the changed files). Returns, per changed file, the duplicated added lines
// and the clones found.
export function detectClones(changed, corpus, w) {
  const dict = new Map();
  const idOf = (v) => {
    let x = dict.get(v);
    if (x === undefined) dict.set(v, (x = dict.size + 1));
    return x;
  };
  const targets = new Map();
  const perFile = new Map();
  for (const c of changed) {
    const toks = tokenize(c.text, langOf(c.file));
    const ids = toks.map((x) => idOf(x.v));
    const hashes = windowHashes(ids, w);
    perFile.set(c.file, { toks, matched: new Array(hashes.length).fill(null) });
    hashes.forEach((h, i) => {
      let touches = false;
      for (let k = i; k < i + w && !touches; k++) touches = c.added.has(toks[k].line);
      if (!touches) return;
      let list = targets.get(h);
      if (!list) targets.set(h, (list = []));
      list.push({ file: c.file, i });
    });
  }
  if (!targets.size) return perFile;
  for (const f of corpus) {
    const toks = tokenize(f.text, langOf(f.file));
    const hashes = windowHashes(
      toks.map((x) => idOf(x.v)),
      w,
    );
    hashes.forEach((h, j) => {
      const list = targets.get(h);
      if (!list) return;
      for (const tgt of list) {
        if (tgt.file === f.file && Math.abs(tgt.i - j) < w) continue;
        const pf = perFile.get(tgt.file);
        if (pf.matched[tgt.i] && pf.matched[tgt.i].file !== tgt.file) continue;
        pf.matched[tgt.i] = { file: f.file, line: toks[j].line, endLine: toks[j + w - 1].line };
      }
    });
  }
  return perFile;
}

// summarize turns matched windows into duplicated added lines and clone fragments.
function summarize(changedEntry, pf, w) {
  const dupLines = new Set();
  const clones = [];
  let cur = null;
  pf.matched.forEach((m, i) => {
    if (!m) {
      cur = null;
      return;
    }
    const startLine = pf.toks[i].line;
    const endLine = pf.toks[i + w - 1].line;
    for (let l = startLine; l <= endLine; l++) if (changedEntry.added.has(l)) dupLines.add(l);
    if (cur && cur.other === m.file && i === cur.lastI + 1) {
      cur.end = endLine;
      cur.otherEnd = Math.max(cur.otherEnd, m.endLine);
      cur.lastI = i;
      cur.tokens++;
    } else {
      cur = { start: startLine, end: endLine, other: m.file, otherLine: m.line, otherEnd: m.endLine, lastI: i, tokens: w };
      clones.push(cur);
    }
  });
  return { dupLines, clones };
}

function nonBlankAdded(root, file, added) {
  let text;
  try {
    text = readFileSync(join(root, file), "utf8");
  } catch {
    return new Set();
  }
  const lines = text.split("\n");
  return new Set([...added].filter((n) => (lines[n - 1] || "").trim() !== ""));
}

export async function run(ctx) {
  const res = newResult(id);
  const changed = [];
  for (const [file, d] of ctx.diff) {
    if (!isSourceFile(file) || isTestFile(file) || SKIP_DIRS.test(file) || !d.addedCount) continue;
    const added = nonBlankAdded(ctx.root, file, d.added);
    if (added.size) changed.push({ file, added });
  }
  const newLines = changed.reduce((s, c) => s + c.added.size, 0);
  res.metrics.new_lines = newLines;
  res.metrics.threshold = ctx.cfg.dup_max;
  res.metrics.min_tokens = ctx.cfg.dup_min_tokens;
  if (!newLines) {
    res.tool = "thulu-gates/duplication";
    res.status = "pass";
    res.summary = t("duplication.no_new_lines");
    return res;
  }
  let results;
  const jscpd = ctx.toolbox.find("jscpd");
  if (jscpd) {
    await use(ctx, res, "jscpd", jscpd);
    results = await viaJscpd(ctx, res, changed);
  }
  if (!results) {
    res._tools = res._tools.filter((x) => x.name !== "jscpd");
    res._tools.push({ name: "thulu-gates/duplication", version: "" });
    results = await builtin(ctx, changed);
    res.metrics.engine = "builtin";
  } else res.metrics.engine = "jscpd";

  // The gate is the percentage (dup_max over at least coverage_min_lines new lines); each clone is
  // listed so the author can find it, and blocks only when the total is over the bar. A file whose
  // duplicated lines did not grow against base adds nothing to the total: its clones were already
  // there.
  const raw = results.reduce((s, r) => s + r.dupLines.size, 0);
  const growth = raw ? await growthOverBase(ctx, res, changed, results) : null;
  let dup = 0;
  for (const r of results) {
    const g = growth?.get(r.file);
    r.newDup = g === undefined || g === null ? r.dupLines.size : Math.min(r.dupLines.size, g);
    dup += r.newDup;
  }
  const pct = Math.round((dup / newLines) * 1000) / 10;
  Object.assign(res.metrics, { duplicated_lines: dup, percent: pct });
  if (raw !== dup) Object.assign(res.metrics, { duplicated_lines_with_base: raw, percent_with_base: Math.round((raw / newLines) * 1000) / 10 });
  const over = pct > ctx.cfg.dup_max && newLines >= ctx.cfg.coverage_min_lines;
  let crossClones = 0;
  for (const r of results) {
    for (const c of r.clones) {
      const cross = c.other !== r.file;
      if (cross) crossClones++;
      const f = {
        rule: cross ? "new-clone" : "new-clone-same-file",
        severity: over ? "medium" : "low",
        file: r.file,
        line: c.start,
        message: t("duplication.clone", { start: c.start, end: c.end, other: c.other, otherStart: c.otherLine, otherEnd: c.otherEnd }),
        blocking: over,
      };
      if (r.newDup === 0) {
        preexisting(f);
        f.rule = "clone-on-base";
        f.severity = "low";
        f.message = t("duplication.clone_base", { start: c.start, end: c.end, other: c.other, otherStart: c.otherLine, otherEnd: c.otherEnd });
      }
      res.findings.push(f);
    }
  }
  res.metrics.clones = res.findings.filter((f) => !f.preexisting).length;
  res.metrics.cross_file_clones = crossClones;
  if (over) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("duplication.fail", { pct, dup, total: newLines, max: ctx.cfg.dup_max, clones: res.findings.length });
  } else if (res.findings.length || pct > ctx.cfg.dup_max) {
    res.status = "warn";
    res.summary = t("duplication.warn", { pct, dup, total: newLines, max: ctx.cfg.dup_max });
  } else {
    res.status = "pass";
    res.summary = t("duplication.ok", { pct, total: newLines, max: ctx.cfg.dup_max });
  }
  if (res.metrics.base_not_compared && over) res.summary = `${res.summary}; ${t("common.base_not_compared")}`;
  return notePreexisting(res);
}

// countDup counts each file's duplicated lines, whole file, against corpus.
function countDup(entries, corpus, w) {
  const all = entries.map((e) => ({ ...e, added: new Set(e.text.split("\n").map((_, i) => i + 1)) }));
  const perFile = detectClones(all, corpus, w);
  return new Map(all.map((e) => [e.file, summarize(e, perFile.get(e.file), w).dupLines.size]));
}

// readCorpus reads the source files of the given languages under dir.
function readCorpus(dir, files, langs) {
  const corpus = [];
  for (const f of files.filter((x) => langs.has(langOf(x)) && !SKIP_DIRS.test(x) && !isTestFile(x)).slice(0, MAX_FILES)) {
    const abs = join(dir, f);
    try {
      if (statSync(abs).size > MAX_FILE_BYTES) continue;
      corpus.push({ file: f, text: readFileSync(abs, "utf8") });
    } catch {
      // deleted or unreadable
    }
  }
  return corpus;
}

// growthOverBase answers, per changed file with clones, how many more duplicated lines it has at
// head than at base (whole file, against the whole repo at each commit). null for a file whose
// base count is unknown; a new file grows by all of its duplicated lines.
async function growthOverBase(ctx, res, changed, results) {
  const w = ctx.cfg.dup_min_tokens;
  const files = results.filter((r) => r.dupLines.size).map((r) => r.file);
  const langs = new Set(files.map(langOf));
  const out = new Map();
  if (res.metrics.engine === "jscpd") {
    const version = res._tools.find((x) => x.name === "jscpd")?.version || "";
    const { value } = await ctx.cache.memo(["duplication", "jscpd", version, w], async () => {
      const baseRoot = await ctx.baseWorktree();
      if (!baseRoot) return { value: undefined };
      const totals = await jscpdTotals(ctx, res, baseRoot, join(ctx.out, "raw", "jscpd-base"));
      return { value: totals ? Object.fromEntries(totals) : undefined };
    });
    if (!value) {
      res.metrics.base_not_compared = true;
      return null;
    }
    for (const f of files) {
      const d = ctx.diff.get(f);
      const head = res._jscpdTotals?.get(f) ?? 0;
      out.set(f, d.status === "A" ? head : Math.max(0, head - (value[d.oldPath || f] ?? 0)));
    }
    return out;
  }
  // Built-in detector: head counts against the head corpus, base counts per file against base's.
  const headCorpus = readCorpus(ctx.root, await lsFiles(ctx.run, ctx.root), langs);
  const entries = files.map((f) => ({ file: f, text: headCorpus.find((c) => c.file === f)?.text ?? readFileSync(join(ctx.root, f), "utf8") }));
  const headCounts = countDup(entries, headCorpus, w);
  const before = new Map();
  const todo = [];
  for (const f of files) {
    const d = ctx.diff.get(f);
    if (d.status === "A") continue;
    const hit = ctx.cache.get(["duplication", "builtin", w, d.oldPath || f]);
    if (hit !== undefined) before.set(f, hit);
    else todo.push(f);
  }
  if (todo.length) {
    const baseRoot = await ctx.baseWorktree();
    if (!baseRoot) {
      res.metrics.base_not_compared = true;
      return null;
    }
    const listing = await ctx.run("git", ["ls-tree", "-r", "-z", "--name-only", ctx.base], { cwd: ctx.root, timeoutMs: 120_000 });
    const baseCorpus = readCorpus(baseRoot, listing.stdout.split("\0").filter(Boolean), langs);
    const want = todo.map((f) => ({ f, old: ctx.diff.get(f).oldPath || f })).filter((x) => baseCorpus.some((c) => c.file === x.old));
    const counts = countDup(
      want.map((x) => ({ file: x.old, text: baseCorpus.find((c) => c.file === x.old).text })),
      baseCorpus,
      w,
    );
    for (const x of want) {
      ctx.cache.set(["duplication", "builtin", w, x.old], counts.get(x.old));
      before.set(x.f, counts.get(x.old));
    }
  }
  for (const f of files) {
    const d = ctx.diff.get(f);
    if (d.status === "A") out.set(f, headCounts.get(f));
    else out.set(f, before.has(f) ? Math.max(0, headCounts.get(f) - before.get(f)) : null);
  }
  return out;
}

async function builtin(ctx, changed) {
  const langs = new Set(changed.map((c) => langOf(c.file)));
  const files = (await lsFiles(ctx.run, ctx.root))
    .filter((f) => langs.has(langOf(f)) && !SKIP_DIRS.test(f) && !isTestFile(f))
    .slice(0, MAX_FILES);
  const corpus = [];
  const seen = new Set();
  for (const f of [...changed.map((c) => c.file), ...files]) {
    if (seen.has(f)) continue;
    seen.add(f);
    const abs = join(ctx.root, f);
    try {
      if (statSync(abs).size > MAX_FILE_BYTES) continue;
      corpus.push({ file: f, text: readFileSync(abs, "utf8") });
    } catch {
      // deleted or unreadable
    }
  }
  const byFile = new Map(corpus.map((c) => [c.file, c.text]));
  const entries = changed.filter((c) => byFile.has(c.file)).map((c) => ({ ...c, text: byFile.get(c.file) }));
  const perFile = detectClones(entries, corpus, ctx.cfg.dup_min_tokens);
  return entries.map((e) => ({ file: e.file, ...summarize(e, perFile.get(e.file), ctx.cfg.dup_min_tokens) }));
}

// viaJscpd runs jscpd over the repo and keeps the clones that touch added lines. Returns null when
// jscpd produced no readable report, so the built-in detector takes over.
// jscpdArgs are the flags of every jscpd run, at head and at base.
async function jscpdArgs(ctx, outDir) {
  // jscpd 5 reads .gitignore by default and refuses --gitignore (exit 2, no report); 3 and 4 need
  // the flag to leave ignored files out.
  const major = Number(/^(\d+)\./.exec(await ctx.toolbox.version("jscpd", ctx.toolbox.find("jscpd")))?.[1]);
  return [
    "--silent",
    "--reporters",
    "json",
    "--output",
    outDir,
    "--min-tokens",
    String(ctx.cfg.dup_min_tokens),
    ...(major >= 5 ? [] : ["--gitignore"]),
    "--ignore",
    "**/node_modules/**,**/vendor/**,**/dist/**,**/build/**,**/target/**,**/testdata/**",
    ".",
  ];
}

function jscpdSide(root, s) {
  return {
    file: relTo(root, s?.name || s?.path || ""),
    start: s?.start ?? s?.startLoc?.line ?? 0,
    end: s?.end ?? s?.endLoc?.line ?? 0,
  };
}

// totalsOf counts, per file, the distinct lines that sit in a clone of a jscpd report.
function totalsOf(root, doc) {
  const lines = new Map();
  for (const d of doc.duplicates || doc.clones || []) {
    for (const x of [jscpdSide(root, d.firstFile || d.first), jscpdSide(root, d.secondFile || d.second)]) {
      let set = lines.get(x.file);
      if (!set) lines.set(x.file, (set = new Set()));
      for (let l = x.start; l <= x.end; l++) set.add(l);
    }
  }
  return new Map([...lines].map(([f, set]) => [f, set.size]));
}

// jscpdTotals runs jscpd over the base checkout and returns the duplicated lines per file. null
// when jscpd left no report.
async function jscpdTotals(ctx, res, root, outDir) {
  fresh(outDir);
  const r = await exec(ctx, res, "jscpd", await jscpdArgs(ctx, outDir), { cwd: root });
  const reportPath = join(outDir, "jscpd-report.json");
  const doc = !r.timedOut && existsSync(reportPath) ? tryJSON(readFileSync(reportPath, "utf8")) : undefined;
  return doc ? totalsOf(root, doc) : null;
}

async function viaJscpd(ctx, res, changed) {
  const outDir = join(ctx.out, "raw", "jscpd");
  fresh(outDir);
  const r = await exec(ctx, res, "jscpd", await jscpdArgs(ctx, outDir));
  const reportPath = join(outDir, "jscpd-report.json");
  const doc = !r.timedOut && existsSync(reportPath) ? tryJSON(readFileSync(reportPath, "utf8")) : undefined;
  if (!doc) {
    // The built-in detector takes over; the reader still learns jscpd did not run.
    res.metrics.jscpd_error = r.timedOut ? t("common.timeout", { seconds: ctx.timeoutMs(id) / 1000 }) : firstLine(r.stderr || r.stdout) || `exit ${r.code}`;
    return null;
  }
  res._jscpdTotals = totalsOf(ctx.root, doc);
  const byFile = new Map(changed.map((c) => [c.file, { file: c.file, dupLines: new Set(), clones: [] }]));
  const side = (x) => jscpdSide(ctx.root, x);
  for (const d of doc.duplicates || doc.clones || []) {
    const a = side(d.firstFile || d.first);
    const b = side(d.secondFile || d.second);
    for (const [x, y] of [
      [a, b],
      [b, a],
    ]) {
      const entry = byFile.get(x.file);
      if (!entry) continue;
      const c = changed.find((cc) => cc.file === x.file);
      let touched = false;
      for (let l = x.start; l <= x.end; l++) {
        if (c.added.has(l)) {
          entry.dupLines.add(l);
          touched = true;
        }
      }
      if (touched) entry.clones.push({ start: x.start, end: x.end, other: y.file, otherLine: y.start, otherEnd: y.end });
    }
  }
  return [...byFile.values()];
}
