// typecheck: the compiler or type checker of each stack. When it reports errors, the same checker
// runs on the base checkout (cached per base commit) and only the errors base does not have block,
// in changed files or elsewhere (a signature change can break an untouched caller). Errors base
// already had are reported as pre-existing.

import { join } from "node:path";

import { t } from "../i18n.js";
import { errorLine, relTo, tryJSON } from "../util.js";
import { absDir, exec, javaBuild, markPreexisting, missingTool, newResult, notePreexisting, skip, timedOut, use } from "./common.js";
import { pyPrefix } from "./tests.js";

export const id = "typecheck";

// Each runner returns {errors: [{file, line, code, message}], ran} or {missing}.
const runners = {
  async ts(ctx, res, p, dir) {
    if (!p.tsconfig) return {};
    const bin = ctx.toolbox.find("tsc", dir);
    if (!bin) return { missing: "tsc" };
    await use(ctx, res, "tsc", bin);
    return {
      ran: true,
      command: [bin, ["--noEmit", "-p", "tsconfig.json", "--pretty", "false"]],
      parse: (r, base) => parseTsc(r.stdout, base),
    };
  },
  async python(ctx, res, p, dir) {
    const prefix = pyPrefix(ctx, p);
    if (p.pyrightConfig && ctx.toolbox.find("pyright")) {
      await use(ctx, res, "pyright", ctx.toolbox.find("pyright"));
      return { ran: true, command: ["pyright", ["--outputjson"]], parse: (r, base) => parsePyright(r.stdout, base) };
    }
    if (!p.mypyConfig) return {};
    if (!prefix.length && !ctx.toolbox.find("mypy")) return { missing: "mypy" };
    await use(ctx, res, "mypy", ctx.toolbox.find("mypy") || prefix[0]);
    const cmd = prefix.length ? [prefix[0], [...prefix.slice(1), "mypy"]] : ["mypy", []];
    cmd[1].push("--show-error-codes", "--no-color-output", "--no-error-summary", "--hide-error-context", ".");
    return { ran: true, command: cmd, parse: (r, base) => parseMypy(r.stdout, base) };
  },
  async go(ctx, res) {
    if (!ctx.toolbox.find("go")) return { missing: "go" };
    await use(ctx, res, "go build", ctx.toolbox.find("go"));
    return { ran: true, command: ["go", ["build", "./..."]], parse: (r, base) => parseColonErrors(r.stderr, base) };
  },
  async rust(ctx, res) {
    if (!ctx.toolbox.find("cargo")) return { missing: "cargo" };
    await use(ctx, res, "cargo check", ctx.toolbox.find("cargo"));
    return { ran: true, command: ["cargo", ["check", "--all-targets", "--message-format=short"]], parse: (r, base) => parseColonErrors(r.stderr, base, /^error\b/) };
  },
  async java(ctx, res, p, dir) {
    const { tool, bin } = javaBuild(ctx, p);
    if (!bin) return { missing: tool };
    await use(ctx, res, tool, ctx.toolbox.find(tool) || join(dir, p.wrapper));
    const args = p.manager === "maven" ? ["-B", "-q", "-DskipTests", "test-compile"] : ["compileTestJava", "-q"];
    return { ran: true, command: [bin, args], parse: (r, base) => parseJavac(r.stdout + "\n" + r.stderr, base) };
  },
};
runners.js = async () => ({});

export function parseTsc(text, base) {
  const out = [];
  for (const m of text.matchAll(/^(.+?)\((\d+),\d+\): error (TS\d+): (.+)$/gm)) out.push({ file: join(base, m[1]), line: Number(m[2]), code: m[3], message: m[4] });
  return out;
}

export function parseMypy(text, base) {
  const out = [];
  for (const m of text.matchAll(/^(.+?):(\d+)(?::\d+)?: error: (.+?)(?:\s+\[([\w-]+)\])?$/gm)) out.push({ file: join(base, m[1]), line: Number(m[2]), code: m[4] || "mypy", message: m[3] });
  return out;
}

function parsePyright(text, base) {
  const doc = tryJSON(text);
  return (doc?.generalDiagnostics || [])
    .filter((d) => d.severity === "error")
    .map((d) => ({ file: d.file?.startsWith("/") ? d.file : join(base, d.file || ""), line: (d.range?.start?.line ?? 0) + 1, code: d.rule || "pyright", message: d.message }));
}

export function parseColonErrors(text, base, mustMatch) {
  const out = [];
  for (const m of text.matchAll(/^(?:\.\/)?([^\s:]+\.\w+):(\d+):(?:\d+:)?\s*(.+)$/gm)) {
    if (mustMatch && !mustMatch.test(m[3])) continue;
    const code = /error\[(E\d+)\]/.exec(m[3])?.[1] || "compile";
    out.push({ file: join(base, m[1]), line: Number(m[2]), code, message: m[3] });
  }
  return out;
}

function parseJavac(text, base) {
  const out = [];
  for (const m of text.matchAll(/\[ERROR\]\s+(\S+\.(?:java|kt)):\[(\d+)[,:]\d+\]\s*(.+)$/gm)) out.push({ file: m[1].startsWith("/") ? m[1] : join(base, m[1]), line: Number(m[2]), code: "javac", message: m[3] });
  return out;
}

export { linkNodeModules } from "./common.js";

const key = (e) => `${e.file}\0${e.code}\0${e.message}`;

// baseErrors runs the same checker on the project's base checkout, once per base commit, checker
// and version: the second run on the same base reads it from the cache. null when base could not
// be checked or its run did not finish.
async function baseErrors(ctx, res, p, dir, spec) {
  const version = res._tools.map((x) => x.version).join(",");
  const parts = ["typecheck", p.dir, p.stack, version, spec.command[1]];
  const { value } = await ctx.cache.memo(parts, async () => {
    const baseDir = await ctx.baseProjectDir(p);
    if (!baseDir) return { value: undefined };
    const baseRoot = p.dir ? baseDir.slice(0, -p.dir.length - 1) : baseDir;
    const command = spec.command[0].startsWith(dir) ? spec.command[0].replace(dir, baseDir) : spec.command[0];
    const br = await exec(ctx, res, command, spec.command[1], { cwd: baseDir });
    if (br.timedOut) return { value: undefined };
    const errors = spec.parse(br, baseDir).map((e) => ({ file: relTo(baseRoot, e.file), code: e.code, message: e.message }));
    // A checker that failed without one error to show broke for another reason (a missing
    // package); the comparison would be against nothing.
    if (br.code !== 0 && !errors.length) return { value: undefined, cache: false };
    return { value: { errors } };
  });
  if (!value) return null;
  // A renamed file: base's errors are reported under its old name.
  const renamed = new Map([...ctx.diff.values()].filter((d) => d.oldPath && d.oldPath !== d.path).map((d) => [d.oldPath, d.path]));
  return value.errors.map((e) => ({ ...e, file: renamed.get(e.file) || e.file }));
}

export async function run(ctx) {
  const res = newResult(id);
  if (!ctx.projects.length) return skip(res, t("typecheck.no_projects"));
  const missing = new Set();
  let ran = 0;
  for (const p of ctx.projects) {
    const runner = runners[p.stack];
    if (!runner) continue;
    const dir = absDir(ctx, p.dir);
    const spec = await runner(ctx, res, p, dir);
    if (spec.missing) missing.add(spec.missing);
    if (!spec.ran) continue;
    ran++;
    const r = await exec(ctx, res, spec.command[0], spec.command[1], { cwd: dir });
    if (r.timedOut) return timedOut(res, ctx.timeoutMs(id) / 1000, false);
    const errors = spec.parse(r, dir).map((e) => ({ ...e, file: relTo(ctx.root, e.file) }));
    if (r.code !== 0 && !errors.length) {
      res.findings.push({ rule: "typecheck", severity: "high", file: "", line: 0, message: errorLine(r.stderr + "\n" + r.stdout) || `exit ${r.code}`, blocking: true });
      continue;
    }
    if (!errors.length) continue;
    // Every error is compared with base, the ones in changed files too: a file the change touched
    // can carry errors it did not cause. Counted per file, code and message (no line: lines move).
    const base = await baseErrors(ctx, res, p, dir, spec);
    if (!base) res.metrics.base_not_compared = true;
    const findings = errors.map((e) => ({ rule: e.code, severity: "high", file: e.file, line: e.line, message: e.message, blocking: true }));
    if (base) markPreexisting(ctx, findings, base.map((e) => ({ rule: e.code, file: e.file, message: e.message })), (f) => `${f.file}\0${f.rule}\0${f.message}`);
    for (const f of findings) {
      // Errors outside the change that base already had are only counted.
      if (f.preexisting && !ctx.diff.has(f.file)) res.metrics.preexisting_outside = (res.metrics.preexisting_outside || 0) + 1;
      else res.findings.push(f);
    }
  }
  if (!ran) return missing.size ? missingTool(res, [...missing]) : skip(res, t("typecheck.nothing_to_check"));
  const blocking = res.findings.filter((f) => f.blocking).length;
  res.status = blocking ? "fail" : res.findings.length ? "warn" : "pass";
  res.blocking = blocking > 0;
  res.summary = blocking ? t("typecheck.errors", { count: blocking }) : t("typecheck.clean");
  if (res.metrics.base_not_compared && blocking) res.summary = `${res.summary}; ${t("common.base_not_compared")}`;
  if (missing.size) res.metrics.not_run = [...missing];
  return notePreexisting(res);
}
