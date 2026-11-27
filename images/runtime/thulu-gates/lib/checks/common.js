// Shared plumbing for checks: the result record, a run wrapper that logs every command, and the
// rules that turn findings into a status.

import { existsSync, lstatSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { basename, dirname, join, relative } from "node:path";

import { shellQuote } from "../exec.js";
import { t } from "../i18n.js";
import { installHint } from "../tools.js";

export function newResult(id) {
  return {
    id,
    tool: "",
    version: "",
    command: "",
    exit_code: null,
    duration_ms: 0,
    status: "skip",
    blocking: false,
    summary: "",
    metrics: {},
    findings: [],
    sarif: null,
    _tools: [],
    _commands: [],
    _exitCodes: [],
  };
}

// use records that a tool took part in the check, with its version.
export async function use(ctx, res, name, path) {
  if (res._tools.some((x) => x.name === name)) return;
  const version = path ? await ctx.toolbox.version(name, path) : "";
  res._tools.push({ name, version });
}

// exec runs one command for a check and records it. cwd is absolute; the logged command carries
// a `cd` when it is not the repo root, so the report's command is copy-pasteable.
export async function exec(ctx, res, cmd, args, opts = {}) {
  const cwd = opts.cwd || ctx.root;
  const rel = relative(ctx.root, cwd);
  // Log a tool found on PATH by its name, so the command reads the same on any machine.
  const shown = cmd.startsWith("/") && ctx.toolbox.find(basename(cmd)) === cmd ? basename(cmd) : cmd;
  const line = shellQuote(shown, args);
  const where = rel.startsWith("..") ? cwd : rel;
  res._commands.push(where ? `(cd ${shellQuote(where)} && ${line})` : line);
  const timeoutMs = opts.timeoutMs ?? ctx.timeoutMs(res.id);
  const r = await ctx.run(cmd, args, { cwd, timeoutMs, env: opts.env, input: opts.input, stallMs: opts.stallMs, watchFile: opts.watchFile });
  res._exitCodes.push(r.code);
  res.duration_ms += r.durationMs || 0;
  ctx.log(`  $ ${res._commands[res._commands.length - 1]} -> ${r.stalled ? "stalled" : r.timedOut ? "timeout" : r.code}`);
  return r;
}

// fresh removes an artifact a previous run left in a reused --out before the tool writes it
// again, so a tool that fails without writing can never be read as the old, passing report.
export function fresh(...paths) {
  for (const p of paths) rmSync(p, { recursive: true, force: true });
}

// guardPaths is for tools that can only write inside the project (Stryker's sandbox and report,
// mutmut's cache): Thulu commits whatever a run leaves in the repo. It remembers which of rels
// exist under dir now; the function it returns deletes the ones that did not and writes back the
// files that did, so the repo ends as the tool found it.
export function guardPaths(dir, rels) {
  const before = rels.map((rel) => {
    const abs = join(dir, rel);
    let st = null;
    try {
      st = lstatSync(abs);
    } catch {
      st = null;
    }
    return { abs, existed: st !== null, content: st?.isFile() ? readFileSync(abs) : null };
  });
  // Each path on its own: a tool that removed a file's directory must not stop the rest from being
  // restored, nor turn the check's result into an exception from a finally block.
  return () => {
    for (const b of before) {
      try {
        if (!b.existed) rmSync(b.abs, { recursive: true, force: true });
        else if (b.content !== null) {
          mkdirSync(dirname(b.abs), { recursive: true });
          writeFileSync(b.abs, b.content);
        }
      } catch {
        // Left as the tool did; the verifier's git status step lists it.
      }
    }
  };
}

export function skip(res, summary) {
  res.status = "skip";
  res.blocking = false;
  res.summary = summary;
  return res;
}

export function missingTool(res, names) {
  const list = Array.isArray(names) ? names : [names];
  return skip(
    res,
    t("common.missing_tool", { tools: list.join(", "), install: list.map(installHint).filter(Boolean).join("; ") }),
  );
}

// timeout handling: a check whose job is to block (tests, lint, types, security) reports an error
// on timeout, because "not verified" must not read as "passed". Advisory checks (mutation, perf)
// report skip with the reason.
export function timedOut(res, seconds, advisory) {
  res.status = advisory ? "skip" : "error";
  res.blocking = false;
  res.metrics.timed_out = true;
  res.metrics.not_verified = true;
  res.summary = t("common.timeout", { seconds });
  return res;
}

// markPreexisting compares a check's findings at head with the same tool's at base and marks as
// pre-existing the ones base already had, counted per key: three on head and two on base for the
// same key leave one new. Head findings outside the lines the change added are matched first, so
// a finding the change did not touch never pushes one it did into "new". Pre-existing findings stay
// in the report and never block.
export function markPreexisting(ctx, head, base, key) {
  const left = new Map();
  for (const b of base) left.set(key(b), (left.get(key(b)) || 0) + 1);
  const onAdded = (f) => {
    const d = ctx.diff.get(f.file);
    return Boolean(d && (!f.line || d.added.has(f.line)));
  };
  const order = [...head.filter((f) => !onAdded(f)), ...head.filter(onAdded)];
  for (const f of order) {
    const k = key(f);
    const n = left.get(k) || 0;
    if (!n) continue;
    left.set(k, n - 1);
    preexisting(f);
  }
  return head;
}

export function preexisting(f) {
  f.preexisting = true;
  f.blocking = false;
  if (f.severity === "high" || f.severity === "critical") f.severity = "medium";
  return f;
}

// notePreexisting counts the pre-existing findings and says so in the summary, so a check that
// passes because every finding was already on base never reads as clean.
export function notePreexisting(res) {
  const n = res.findings.filter((f) => f.preexisting).length + (res.metrics.preexisting_outside || 0);
  if (!n) return res;
  res.metrics.preexisting = n;
  res.summary = `${res.summary}; ${t("common.preexisting", { count: n })}`;
  return res;
}

// settle sets status and blocking from the findings unless the check already decided.
export function settle(res, { passSummary, failSummary, warnSummary } = {}) {
  const blocking = res.findings.filter((f) => f.blocking);
  const warnings = res.findings.filter((f) => !f.blocking && !f.preexisting);
  if (blocking.length) {
    res.status = "fail";
    res.blocking = true;
    res.summary = failSummary || t("common.blocking_findings", { count: blocking.length });
  } else if (warnings.length) {
    res.status = "warn";
    res.summary = warnSummary || t("common.warning_findings", { count: warnings.length });
  } else if (res.findings.length) {
    res.status = "warn";
    res.summary = passSummary || t("common.no_findings");
  } else {
    res.status = "pass";
    res.summary = passSummary || t("common.no_findings");
  }
  return res;
}

// onlyAdded keeps findings that sit on lines this diff added. Findings without a line (a whole
// file) stay when the file changed.
export function onlyAdded(ctx, findings) {
  return findings.filter((f) => {
    const d = ctx.diff.get(f.file);
    if (!d) return false;
    if (!f.line) return true;
    return d.added.has(f.line);
  });
}

export function absDir(ctx, dir) {
  return dir ? join(ctx.root, dir) : ctx.root;
}

// filesIn returns the changed files of a project as paths relative to its own directory.
export function projectFiles(project, pred = () => true) {
  return project.files.filter(pred).map((f) => (project.dir ? f.slice(project.dir.length + 1) : f));
}

// javaBuild picks the project's build command: its wrapper (mvnw, gradlew) or the tool on PATH.
export function javaBuild(ctx, p) {
  const tool = p.manager === "maven" ? "mvn" : "gradle";
  const bin = p.wrapper || (ctx.toolbox.find(tool) ? tool : null);
  return { tool, bin };
}

export function isBlockingSeverity(sev) {
  return sev === "critical" || sev === "high";
}

// linkNodeModules gives the base worktree the head's installed packages: the project's own
// node_modules and every one above it up to the repo root, where workspaces hoist them. Without
// them the base run fails on imports and each preexisting error elsewhere would count as new.
export function linkNodeModules(root, baseRoot, dir) {
  let d = dir;
  for (;;) {
    const from = join(root, d, "node_modules");
    const to = join(baseRoot, d, "node_modules");
    if (existsSync(from) && !existsSync(to) && existsSync(join(baseRoot, d))) {
      try {
        symlinkSync(from, to, "dir");
      } catch {
        // left without packages: its outside errors count as new, the safe side
      }
    }
    if (!d) return;
    d = dirname(d) === "." ? "" : dirname(d);
  }
}

const VENVS = [".venv", "venv"];

// linkBaseDeps gives a project's base checkout what the head has installed and git does not
// track: node_modules (with the hoisted ones above it) and a Python virtualenv.
export function linkBaseDeps(root, baseRoot, p) {
  if (p.stack === "ts" || p.stack === "js") linkNodeModules(root, baseRoot, p.dir);
  if (p.stack === "python") {
    for (const v of VENVS) {
      const from = join(root, p.dir, v);
      const to = join(baseRoot, p.dir, v);
      if (!existsSync(from) || existsSync(to) || !existsSync(join(baseRoot, p.dir))) continue;
      try {
        symlinkSync(from, to, "dir");
      } catch {
        // left without it: the base run reports what it can
      }
    }
  }
}
