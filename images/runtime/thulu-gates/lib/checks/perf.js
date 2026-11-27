// perf: benchmarks of base and head on the same machine, interleaved, with a number of rounds
// fixed before starting. Go benchmarks in the changed packages run with
// go test -bench (benchstat output saved when installed); any stack can declare perf.command in
// .thulu/gates.json, timed with hyperfine when installed. Blocks only on a regression that is
// significant (Mann-Whitney p < alpha) and larger than perf.threshold_pct (10%); a smaller
// significant regression warns.

import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { hostname, loadavg } from "node:os";
import { dirname, join } from "node:path";

import { shellQuote } from "../exec.js";
import { t } from "../i18n.js";
import { mannWhitney, median, medianCI } from "../stats.js";
import { firstLine, tryJSON } from "../util.js";
import { exec, fresh, missingTool, newResult, skip, timedOut, use } from "./common.js";

export const id = "perf";

// parseGoBench reads `go test -bench` output into "pkg Benchmark" -> [ns/op].
export function parseGoBench(text, into = new Map()) {
  let pkg = "";
  for (const line of text.split("\n")) {
    const p = /^pkg: (\S+)/.exec(line);
    if (p) pkg = p[1];
    const m = /^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([\d.]+) ns\/op/.exec(line);
    if (!m) continue;
    const key = pkg ? `${pkg} ${m[1]}` : m[1];
    if (!into.has(key)) into.set(key, []);
    into.get(key).push(Number(m[2]));
  }
  return into;
}

// compare turns two sample maps into rows with a verdict each.
export function compare(base, head, { alpha, thresholdPct }) {
  const rows = [];
  for (const [name, h] of head) {
    const b = base.get(name);
    if (!b || b.length < 3 || h.length < 3) {
      rows.push({ name, n: h.length, verdict: b ? "too_few" : "new" });
      continue;
    }
    const mb = median(b);
    const mh = median(h);
    const delta = mb ? ((mh - mb) / mb) * 100 : 0;
    const { p } = mannWhitney(b, h);
    const significant = p < alpha;
    let verdict = "no_change";
    if (significant && delta > thresholdPct) verdict = "regression";
    else if (significant && delta > 0) verdict = "minor_regression";
    else if (significant && delta < 0) verdict = "improvement";
    rows.push({
      name,
      n: Math.min(b.length, h.length),
      base_median: mb,
      head_median: mh,
      base_ci: medianCI(b),
      head_ci: medianCI(h),
      delta_pct: Math.round(delta * 10) / 10,
      p: Math.round(p * 10000) / 10000,
      verdict,
    });
  }
  return rows;
}

function hasBenchmarks(dir) {
  try {
    return readdirSync(dir).some((f) => f.endsWith("_test.go") && /^func Benchmark\w*\(/m.test(readFileSync(join(dir, f), "utf8")));
  } catch {
    return false;
  }
}

const TIME_UNITS = { second: 1e9, millisecond: 1e6, microsecond: 1e3, nanosecond: 1 };

// parseHyperfine returns the wall-clock time of each run, in nanoseconds, from hyperfine's
// --export-json: results[0].times in seconds up to 1.x, results[0].measurements[] with
// {time_wall_clock: {value, unit}} in 2.x (schema_version 2).
export function parseHyperfine(doc) {
  const r = doc?.results?.[0];
  if (Array.isArray(r?.times)) return r.times.map((s) => s * 1e9);
  const metric = doc?.primary_metric || "time_wall_clock";
  const out = [];
  for (const m of r?.measurements || []) {
    const v = m?.[metric] ?? m?.time_wall_clock;
    const scale = TIME_UNITS[v?.unit];
    if (typeof v?.value === "number" && scale) out.push(v.value * scale);
  }
  return out;
}

export async function run(ctx) {
  const res = newResult(id);
  if (!ctx.flags.perf) return skip(res, t("perf.not_requested"));
  const rounds = Math.max(3, Number(ctx.cfg.perf?.rounds) || 10);
  const alpha = Number(ctx.cfg.perf?.alpha) || 0.05;
  const thresholdPct = Number(ctx.cfg.perf?.threshold_pct) || 10;

  // Go packages with benchmarks among those the diff touches.
  const goJobs = [];
  for (const p of ctx.projects.filter((x) => x.stack === "go")) {
    const pkgs = new Set();
    for (const f of p.files.filter((f) => f.endsWith(".go"))) {
      const rel = p.dir ? f.slice(p.dir.length + 1) : f;
      const d = dirname(rel) === "." ? "" : dirname(rel);
      if (hasBenchmarks(join(ctx.root, p.dir, d))) pkgs.add(d);
    }
    if (pkgs.size) goJobs.push({ p, pkgs: [...pkgs] });
  }
  const command = ctx.cfg.perf?.command;
  if (!goJobs.length && !command) return skip(res, t("perf.nothing_to_run"));
  if (goJobs.length && !ctx.toolbox.find("go")) return missingTool(res, "go");

  const baseRoot = await ctx.baseWorktree();
  if (!baseRoot) {
    res.status = "error";
    res.summary = t("perf.no_base_worktree");
    return res;
  }
  const raw = join(ctx.out, "raw");
  mkdirSync(raw, { recursive: true });
  const started = Date.now();
  const budget = ctx.timeoutMs(id);
  const baseSamples = new Map();
  const headSamples = new Map();
  const baseText = [];
  const headText = [];

  const hyperfine = command ? ctx.toolbox.find("hyperfine") : null;
  if (goJobs.length) await use(ctx, res, "go test -bench", ctx.toolbox.find("go"));
  if (hyperfine) await use(ctx, res, "hyperfine", hyperfine);
  else if (command) res._tools.push({ name: "thulu-gates/timer", version: "" });

  for (let r = 0; r < rounds; r++) {
    const order = r % 2 === 0 ? ["base", "head"] : ["head", "base"];
    for (const side of order) {
      const root = side === "base" ? baseRoot : ctx.root;
      const samples = side === "base" ? baseSamples : headSamples;
      const remaining = budget - (Date.now() - started);
      if (remaining <= 0) return timedOut(res, budget / 1000, true);
      for (const job of goJobs) {
        const dir = join(root, job.p.dir);
        const pkgs = job.pkgs.filter((d) => existsSync(join(dir, d))).map((d) => (d ? `./${d}` : "."));
        if (!pkgs.length) continue;
        const out = await exec(ctx, res, "go", ["test", "-run=^$", "-bench=.", "-benchmem", "-count=1", ...pkgs], { cwd: dir, timeoutMs: remaining });
        if (out.timedOut) return timedOut(res, budget / 1000, true);
        if (out.code !== 0 && side === "head") {
          res.status = "error";
          res.summary = t("perf.bench_failed", { detail: out.stderr.split("\n")[0] || `exit ${out.code}` });
          return res;
        }
        (side === "base" ? baseText : headText).push(out.stdout);
        parseGoBench(out.stdout, samples);
      }
      if (command) {
        const key = `command: ${command}`;
        if (!samples.has(key)) samples.set(key, []);
        if (hyperfine) {
          const json = join(raw, `hyperfine-${side}-${r}.json`);
          fresh(json);
          // --shell sh: hyperfine 2 refuses a command with shell syntax ("&&", which the cd adds
          // and perf.command may have) unless the shell is chosen; 1.x takes the same flag.
          const hr = await exec(ctx, res, hyperfine, ["--shell", "sh", "--runs", "2", "--warmup", "1", "--export-json", json, "-n", side, `cd ${shellQuote(root)} && ${command}`], { timeoutMs: remaining });
          if (hr.timedOut) return timedOut(res, budget / 1000, true);
          // hyperfine stops at the first failing run and writes no times: without this the side
          // ends up with no samples and the comparison reads "new", which passes.
          if (hr.code !== 0) {
            res.status = "error";
            res.summary = t("perf.command_failed", { side, code: hr.code, detail: firstLine(hr.stderr || hr.stdout) || "-" });
            return res;
          }
          const times = parseHyperfine(existsSync(json) ? tryJSON(readFileSync(json, "utf8")) : undefined);
          // A JSON this parser cannot read would leave the side with no samples, and "too few
          // samples" passes.
          if (!times.length) {
            res.status = "error";
            res.summary = t("perf.hyperfine_no_times", { side, file: json });
            return res;
          }
          samples.get(key).push(...times);
        } else {
          const cr = await exec(ctx, res, "sh", ["-c", command], { cwd: root, timeoutMs: remaining });
          if (cr.timedOut) return timedOut(res, budget / 1000, true);
          if (cr.code !== 0) {
            res.status = "error";
            res.summary = t("perf.command_failed", { side, code: cr.code, detail: firstLine(cr.stderr || cr.stdout) || "-" });
            return res;
          }
          samples.get(key).push(cr.durationMs * 1e6);
        }
      }
    }
  }
  if (baseText.length) {
    writeFileSync(join(raw, "bench-base.txt"), baseText.join("\n"));
    writeFileSync(join(raw, "bench-head.txt"), headText.join("\n"));
    const benchstat = ctx.toolbox.find("benchstat");
    if (benchstat) {
      await use(ctx, res, "benchstat", benchstat);
      const bs = await exec(ctx, res, benchstat, [join(raw, "bench-base.txt"), join(raw, "bench-head.txt")], { timeoutMs: 60_000 });
      writeFileSync(join(raw, "benchstat.txt"), bs.stdout);
      res.metrics.benchstat = "raw/benchstat.txt";
    }
  }
  const rows = compare(baseSamples, headSamples, { alpha, thresholdPct });
  Object.assign(res.metrics, {
    method: t("perf.method", { rounds }),
    rounds,
    alpha,
    threshold_pct: thresholdPct,
    node: hostname(),
    loadavg: loadavg().map((x) => Math.round(x * 100) / 100),
    benchmarks: rows,
  });
  for (const row of rows.filter((x) => x.verdict === "regression" || x.verdict === "minor_regression")) {
    res.findings.push({
      rule: row.verdict === "regression" ? "perf-regression" : "perf-minor-regression",
      severity: row.verdict === "regression" ? "high" : "medium",
      file: "",
      line: 0,
      message: t("perf.row", { name: row.name, delta: row.delta_pct, p: row.p, n: row.n }),
      blocking: row.verdict === "regression",
    });
  }
  const regressions = res.findings.filter((f) => f.blocking).length;
  if (regressions) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("perf.regressions", { count: regressions, threshold: thresholdPct });
  } else if (res.findings.length) {
    res.status = "warn";
    res.summary = t("perf.minor", { count: res.findings.length, threshold: thresholdPct });
  } else {
    res.status = "pass";
    res.summary = t("perf.no_change", { count: rows.length });
  }
  return res;
}
