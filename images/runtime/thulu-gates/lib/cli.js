// thulu-gates CLI: `run` computes base and diff, runs the checks and writes report.json,
// report.md and sarif/<tool>.sarif; `doctor` lists the tools each stack wants. Exit codes for
// run: 0 pass, 1 blocking findings, 2 the tool itself failed (or a check could not run to the
// end).

import { existsSync, mkdirSync, readdirSync, realpathSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, relative, resolve } from "node:path";

import { createBaseCache, projectKey, resolveCacheDir } from "./cache.js";
import { CHECKS, CHECK_IDS, OPT_IN } from "./checks/index.js";
import { linkBaseDeps, newResult } from "./checks/common.js";
import { CONFIG_PATH, loadConfig } from "./config.js";
import { run as realRun, which as realWhich } from "./exec.js";
import { addWorktree, defaultBranch, diffAgainst, dirtyFiles, headSha, isDirty, mergeBase, removeWorktree, repoRoot, revParse, showFile } from "./git.js";
import { setLocale, t } from "./i18n.js";
import { acceptedChecks, loadProfile } from "./profile.js";
import { loadQuarantine } from "./quarantine.js";
import { applyAcceptance, buildReport, publicCheck, renderMarkdown } from "./report.js";
import { createReportStore, plan, reuseKey } from "./reuse.js";
import { toSarif, withFingerprints } from "./sarif.js";
import { declaredVersions, detectProjects, repoCommands } from "./stack.js";
import { createToolbox, doctor } from "./tools.js";
import { readText } from "./util.js";

export const VERSION = "0.1.0";

export function parseArgs(argv) {
  const flags = { critical: [], only: null, skip: [] };
  const rest = [];
  const need = (i, name) => {
    if (i + 1 >= argv.length) throw new UsageError(t("cli.missing_value", { flag: name }));
    return argv[i + 1];
  };
  const num = (v, name) => {
    const n = Number(v);
    if (!Number.isFinite(n)) throw new UsageError(t("cli.not_a_number", { flag: name, value: v }));
    return n;
  };
  for (let i = 0; i < argv.length; i++) {
    let a = argv[i];
    let inline = null;
    if (a.startsWith("--") && a.includes("=")) {
      inline = a.slice(a.indexOf("=") + 1);
      a = a.slice(0, a.indexOf("="));
    }
    const value = (name) => {
      if (inline !== null) return inline;
      const v = need(i, name);
      i++;
      return v;
    };
    switch (a) {
      case "--base":
        flags.base = value(a);
        break;
      case "--out":
        flags.out = value(a);
        break;
      case "--cache-dir":
        flags.cacheDir = value(a);
        break;
      case "--quarantine":
        flags.quarantine = value(a);
        break;
      case "--profile":
        flags.profile = value(a);
        break;
      case "--reuse":
        flags.reuse = true;
        break;
      case "--only":
        flags.only = value(a).split(",").map((s) => s.trim()).filter(Boolean);
        break;
      case "--skip":
        flags.skip = value(a).split(",").map((s) => s.trim()).filter(Boolean);
        break;
      case "--perf":
        flags.perf = true;
        break;
      case "--mutation":
        flags.mutation = true;
        break;
      case "--critical":
        flags.critical.push(value(a));
        break;
      case "--coverage-min":
        flags.coverageMin = num(value(a), a);
        break;
      case "--dup-max":
        flags.dupMax = num(value(a), a);
        break;
      case "--json":
        flags.json = true;
        break;
      case "--quiet":
      case "-q":
        flags.quiet = true;
        break;
      case "--help":
      case "-h":
        flags.help = true;
        break;
      case "--version":
        flags.version = true;
        break;
      default:
        if (a.startsWith("-")) throw new UsageError(t("cli.unknown_flag", { flag: a }));
        rest.push(a);
    }
  }
  for (const idList of [flags.only || [], flags.skip]) {
    for (const x of idList) if (!CHECK_IDS.includes(x)) throw new UsageError(t("cli.unknown_check", { id: x, ids: CHECK_IDS.join(", ") }));
  }
  return { command: rest[0] || null, flags };
}

class UsageError extends Error {}

// realish resolves symlinks in the part of p that exists and keeps the rest as written.
function realish(p) {
  try {
    return realpathSync(p);
  } catch {
    const up = dirname(p);
    return up === p ? p : join(realish(up), basename(p));
  }
}

// defaultOut is where a run without --out writes: under the temp folder TMPDIR names, which is the
// run's own on a runner machine and /tmp in a container, so a host run's reports go away with it.
export function defaultOut(startedAt) {
  return join(tmpdir(), "thulu-gates", stamp(new Date(startedAt)));
}

function stamp(d) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
}

function toolSlug(tool, id) {
  const s = String(tool || `thulu-gates/${id}`)
    .replace(/[^A-Za-z0-9._+-]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s || id;
}

// selected decides which checks run and, for the rest, why not.
export function selection(flags) {
  const out = new Map();
  for (const id of CHECK_IDS) {
    if (flags.only && !flags.only.includes(id)) out.set(id, t("cli.not_selected"));
    else if (flags.skip.includes(id)) out.set(id, t("cli.skipped_by_flag"));
    else out.set(id, null);
  }
  return out;
}

export async function main(argv, deps = {}) {
  const run = deps.run || realRun;
  const which = deps.which || realWhich;
  const stdout = deps.stdout || ((s) => process.stdout.write(s));
  const stderr = deps.stderr || ((s) => process.stderr.write(s));
  const cwd = deps.cwd || process.cwd();
  const env = deps.env || process.env;
  if (deps.locale) setLocale(deps.locale);

  let parsed;
  try {
    parsed = parseArgs(argv);
  } catch (err) {
    if (err instanceof UsageError) {
      stderr(`thulu-gates: ${err.message}\n\n${t("cli.usage")}\n`);
      return 2;
    }
    throw err;
  }
  const { command, flags } = parsed;
  if (flags.version) {
    stdout(`thulu-gates ${VERSION}\n`);
    return 0;
  }
  if (flags.help || !command) {
    stdout(`${t("cli.usage")}\n`);
    return flags.help ? 0 : 2;
  }
  if (command === "doctor") return runDoctor({ run, which, cwd, flags, stdout });
  if (command !== "run") {
    stderr(`thulu-gates: ${t("cli.unknown_command", { command })}\n\n${t("cli.usage")}\n`);
    return 2;
  }
  try {
    return await runGates({ run, which, cwd, env, flags, stdout, stderr, fetchFn: deps.fetch || globalThis.fetch, now: deps.now || Date.now });
  } catch (err) {
    stderr(`thulu-gates: ${err.message}\n`);
    return 2;
  }
}

async function runDoctor({ run, which, cwd, flags, stdout }) {
  let root = cwd;
  try {
    root = await repoRoot(run, cwd);
  } catch {
    // outside a repo: list every stack's tools
  }
  const names = ["go.mod", "package.json", "pyproject.toml", "requirements.txt", "setup.py", "Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts"];
  // The root and one level down, where monorepos keep their packages (web/, api/).
  const dirs = [""];
  try {
    for (const d of readdirSync(root, { withFileTypes: true })) if (d.isDirectory() && !d.name.startsWith(".") && d.name !== "node_modules") dirs.push(d.name);
  } catch {
    // unreadable root: only the root markers
  }
  const markers = dirs.flatMap((d) => names.map((n) => (d ? `${d}/${n}` : n))).filter((m) => existsSync(join(root, m)));
  const projects = detectProjects(root, markers).map((p) => ({ ...p, absDir: join(root, p.dir) }));
  const stacks = [...new Set(projects.map((p) => p.stack))];
  const toolbox = createToolbox({ run, which, root });
  const rows = await doctor({ stacks: stacks.length ? stacks : ["go", "ts", "js", "python", "rust", "java"], toolbox, projects });
  if (flags.json) {
    stdout(JSON.stringify({ stacks, tools: rows }, null, 2) + "\n");
    return 0;
  }
  const lines = [t("doctor.header", { stacks: stacks.length ? stacks.join(", ") : t("doctor.no_stack") }), ""];
  for (const r of rows) {
    const checks = r.checks.length ? ` [${r.checks.join(", ")}]` : "";
    lines.push(r.found ? `  ok       ${r.name}${r.version ? ` (${r.version})` : ""}${checks}` : `  missing  ${r.name}${checks}: ${r.install}`);
  }
  const missing = rows.filter((r) => !r.found).length;
  lines.push("", missing ? t("doctor.missing", { count: missing }) : t("doctor.all_found"));
  stdout(lines.join("\n") + "\n");
  return 0;
}

async function runGates({ run, which, cwd, env, flags, stdout, stderr, fetchFn, now }) {
  const startedAt = now();
  const log = flags.quiet ? () => {} : (s) => stderr(s + "\n");
  const root = await repoRoot(run, cwd);
  const branch = await defaultBranch(run, root);
  let base;
  if (flags.base) {
    base = await revParse(run, root, flags.base);
    if (!base) throw new Error(t("cli.bad_base", { ref: flags.base }));
  } else {
    // A pod's clone holds only the task branch: without either ref, say what to pass instead of
    // failing inside git merge-base.
    let ref = null;
    if (await revParse(run, root, `refs/remotes/origin/${branch}`)) ref = `origin/${branch}`;
    else if (await revParse(run, root, `refs/heads/${branch}`)) ref = branch;
    if (!ref) throw new Error(t("cli.no_base", { branch }));
    base = await mergeBase(run, root, ref);
  }
  const head = await headSha(run, root);
  const dirty = await isDirty(run, root);
  const out = resolve(cwd, flags.out || defaultOut(startedAt));
  // Compared through real paths: git answers the root resolved (/private/var on macOS) while
  // --out keeps whatever symlinks the caller typed.
  const outReal = realish(out);
  if (outReal === root || !relative(outReal, root).startsWith("..")) throw new Error(t("cli.out_contains_repo", { out }));
  mkdirSync(join(out, "sarif"), { recursive: true });
  mkdirSync(join(out, "raw"), { recursive: true });

  const diff = await diffAgainst(run, root, base);
  // An --out inside the repo holds the previous run's report, SARIF and coverage as untracked
  // files: they are this tool's output, not the task's code.
  const outRel = relative(root, outReal);
  if (!outRel.startsWith("..") && !isAbsolute(outRel)) {
    for (const f of [...diff.keys()]) if (f.startsWith(outRel + "/")) diff.delete(f);
  }
  for (const f of await dirtyFiles(run, root)) if (diff.has(f)) diff.get(f).dirty = true;

  // The project's profile comes from Thulu (THULU_GATES_PROFILE), never from the client repo.
  const prof = loadProfile({ flag: flags.profile, env, root, resolvePath: (p) => resolve(cwd, p) });
  for (const w of prof.warnings) log(`thulu-gates: ${t(`cli.${w.key}`, w.vars)}`);
  const { cfg, warnings } = loadConfig({
    baseText: await showFile(run, root, base, CONFIG_PATH),
    headText: readText(join(root, CONFIG_PATH)),
    flags,
    profile: prof.profile,
  });
  for (const w of warnings) log(`thulu-gates: ${t(`cli.${w}`, { path: CONFIG_PATH })}`);
  cfg.profile = prof.profile ? prof.path : null;
  const acceptance = acceptedChecks(cfg.accept_unverified, CHECK_IDS);
  for (const w of acceptance.warnings) log(`thulu-gates: ${t(`cli.${w.key}`, { ...w.vars, path: prof.path || "" })}`);
  const projects = detectProjects(root, [...diff.keys()]);
  const stacks = [...new Set(projects.map((p) => p.stack))];
  const toolbox = createToolbox({ run, which, root });
  // Base results and the quarantine live outside the client repo, per repo.
  const cacheDir = resolveCacheDir(flags.cacheDir, env, cwd);
  const key = await projectKey(run, root);
  const cache = createBaseCache({ dir: join(cacheDir, key), base, log });
  const quarantinePath = flags.quarantine ? resolve(cwd, flags.quarantine) : env.THULU_GATES_QUARANTINE ? resolve(cwd, env.THULU_GATES_QUARANTINE) : join(cacheDir, key, "quarantine.json");
  const quarantine = loadQuarantine(quarantinePath);
  if (quarantine.error) log(`thulu-gates: ${t("cli.quarantine_unreadable", { path: quarantinePath })}`);

  let baseWorktree = null;
  let baseWorktreeTried = false;
  const lookups = new Map();
  const ctx = {
    root,
    base,
    head,
    out,
    cfg,
    diff,
    projects,
    flags: { ...flags, mutation: flags.mutation || Boolean(flags.only?.includes("mutation")), perf: flags.perf || Boolean(flags.only?.includes("perf")) },
    run,
    toolbox,
    fetch: fetchFn,
    now,
    log,
    cache,
    cacheDir,
    quarantine,
    coverage: [],
    results: new Map(),
    sarifNames: new Set(),
    timeoutMs: (id) => 1000 * (cfg.timeouts?.[id] ?? cfg.timeouts?.default ?? 600),
    async baseWorktree() {
      if (!baseWorktreeTried) {
        baseWorktreeTried = true;
        try {
          baseWorktree = await addWorktree(run, root, base);
        } catch (err) {
          log(`thulu-gates: ${t("cli.worktree_failed", { detail: err.message })}`);
        }
      }
      return baseWorktree;
    },
    // baseProjectDir is a project's directory in the base checkout, with the head's installed
    // packages linked in (node_modules, a Python virtualenv): without them a base run fails on
    // imports and every finding base has would count as new. null when base could not be checked
    // out.
    async baseProjectDir(p) {
      const baseRoot = await this.baseWorktree();
      if (!baseRoot) return null;
      linkBaseDeps(root, baseRoot, p);
      return p.dir ? join(baseRoot, p.dir) : baseRoot;
    },
    async lookup(dep, fn) {
      const k = `${dep.ecosystem}\0${dep.name}\0${dep.exact || ""}`;
      if (!lookups.has(k)) lookups.set(k, fn(dep, { fetchFn }));
      return lookups.get(k);
    },
  };

  log(t("cli.start", { base: base.slice(0, 12), head: head.slice(0, 12), files: diff.size, stacks: stacks.join(", ") || "-" }));
  const sel = selection(ctx.flags);
  // Reuse needs a clean tree: uncommitted files are not in the key, so their results would be
  // someone else's.
  const clean = !dirty && ![...diff.values()].some((d) => d.untracked);
  const reports = createReportStore({ dir: join(cacheDir, key, "reports"), log });
  const keyOf = () => reuseKey({ head, base, version: VERSION, cfg, quarantinePath });
  let entry;
  if (flags.reuse || env.THULU_GATES_REUSE === "1") {
    if (!clean) log(`thulu-gates: ${t("cli.reuse_dirty")}`);
    else {
      entry = reports.get(keyOf());
      if (!entry) log(`thulu-gates: ${t("cli.reuse_miss")}`);
    }
  }
  const wanted = (id) => !sel.get(id) && (!OPT_IN.has(id) || Boolean(ctx.flags[id]));
  const { steps, stored } = plan({ ids: CHECK_IDS, wanted, only: entry ? flags.only : null, entry, accepted: acceptance.accepted });
  const results = [];
  const ran = [];
  const reusedIds = [];
  try {
    for (const check of CHECKS) {
      const why = sel.get(check.id);
      const step = steps.get(check.id);
      let res;
      if (step === "reuse") {
        res = restore(ctx, stored.get(check.id), entry);
        ctx.results.set(check.id, res);
        results.push(res);
        ran.push(check.id);
        reusedIds.push(check.id);
        continue;
      }
      if (step === "leave" && why) {
        res = newResult(check.id);
        res.summary = why;
      } else if (OPT_IN.has(check.id) && !ctx.flags[check.id]) {
        res = await check.run(ctx);
      } else {
        log(t("cli.running", { id: check.id }));
        ran.push(check.id);
        try {
          res = await check.run(ctx);
        } catch (err) {
          res = newResult(check.id);
          res.status = "error";
          res.summary = t("cli.check_crashed", { detail: err.message });
          log(err.stack || String(err));
        }
      }
      finalize(ctx, res);
      ctx.results.set(check.id, res);
      results.push(res);
    }
  } finally {
    if (baseWorktree) await removeWorktree(run, root, baseWorktree);
    try {
      quarantine.save();
    } catch (err) {
      log(`thulu-gates: ${t("cli.quarantine_not_saved", { path: quarantinePath, detail: err.message })}`);
    }
  }

  const raw = results.map(publicCheck);
  const rerun = ran.filter((id) => !reusedIds.includes(id));
  // A run that measured nothing new keeps the stored entry's time and folder: that is where the
  // results came from.
  const fromStore = Boolean(entry) && !rerun.length;
  // The tests the quarantine took in belong to the tests result they came with.
  const quarantineAdded = reusedIds.includes("tests")
    ? entry.quarantine_added || []
    : (quarantine.added || []).map((e) => ({ id: e.id, project: e.project || ".", reason: e.reason }));
  if (clean) {
    reports.set(keyOf(), {
      created_at: fromStore ? entry.created_at : new Date(startedAt).toISOString(),
      out: fromStore ? entry.out : out,
      ran,
      checks: raw,
      sarif: Object.fromEntries(raw.filter((c) => c.sarif).map((c) => [c.sarif, readText(join(out, c.sarif)) ?? ""])),
      quarantine_added: quarantineAdded,
    });
  }
  const report = buildReport({
    base,
    head,
    dirty: !clean,
    defaultBranch: branch,
    stacks,
    projects,
    repoCommands: repoCommands(root),
    versions: declaredVersions(root),
    cfg,
    checks: applyAcceptance(raw, acceptance.accepted),
    selection: sel,
    quarantine,
    cacheDir: cache.dir,
    startedAt,
    finishedAt: now(),
    out,
    reused: entry && reusedIds.length ? { from: entry.created_at, out: entry.out, checks: reusedIds, rerun } : null,
  });
  report.quarantine.added = quarantineAdded;
  writeFileSync(join(out, "report.json"), JSON.stringify(report, null, 2) + "\n");
  const md = renderMarkdown(report);
  writeFileSync(join(out, "report.md"), md);
  stdout(md);
  stdout(`\n${t("cli.wrote", { out })}\n`);
  if (report.blocking.length) return 1;
  if (report.errors.length) return 2;
  return 0;
}

// restore brings back a stored check result and writes its SARIF into this run's --out.
function restore(ctx, stored, entry) {
  const res = { ...JSON.parse(JSON.stringify(stored)), _tools: [], _commands: [], _exitCodes: [] };
  if (res.sarif && entry.sarif?.[res.sarif] !== undefined) {
    writeFileSync(join(ctx.out, res.sarif), entry.sarif[res.sarif]);
    ctx.sarifNames.add(basename(res.sarif, ".sarif"));
  } else res.sarif = null;
  return res;
}

// finalize fills the contract fields from what the check recorded and writes its SARIF.
function finalize(ctx, res) {
  if (res._tools.length && !res.tool) res.tool = res._tools.map((x) => x.name).join(", ");
  if (!res.version) res.version = res._tools.map((x) => x.version).filter(Boolean).join(", ");
  res.command = res._commands.join(" ; ");
  const nonZero = res._exitCodes.find((c) => c !== 0);
  res.exit_code = res._exitCodes.length ? (nonZero === undefined ? 0 : nonZero) : null;
  for (const f of res.findings) {
    f.file = f.file || "";
    f.line = f.line || 0;
    f.blocking = Boolean(f.blocking);
  }
  if (res.status === "skip" && !res.findings.length) return;
  withFingerprints(res.tool || res.id, ctx.root, res.findings);
  let slug = toolSlug(res.tool, res.id);
  if (ctx.sarifNames.has(slug)) slug = `${slug}-${res.id}`;
  ctx.sarifNames.add(slug);
  const rel = `sarif/${slug}.sarif`;
  writeFileSync(join(ctx.out, rel), JSON.stringify(toSarif([{ tool: res.tool || `thulu-gates/${res.id}`, version: res.version, findings: res.findings }]), null, 2) + "\n");
  res.sarif = rel;
}
