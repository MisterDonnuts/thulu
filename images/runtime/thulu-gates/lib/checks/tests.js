// tests: runs each touched project's suite once, with coverage when the stack can produce it, and
// leaves the coverage files for coverage_new. A failing test blocks only when the change broke it:
// when the head run has failures or hangs, the same suite runs on the base checkout (cached per
// base commit) and a test that fails or hangs there too is pre-existing. Those go to the
// quarantine, outside the client repo, and later runs leave them out of the suite where the runner
// can exclude one test. A test that hangs is found from the runner's own progress (a reporter
// thulu-gates adds, or the runner's event stream); the suite is cut when it goes quiet for
// timeouts.test_stall seconds and runs again without that test, so the rest still gets verified.

import { existsSync, mkdirSync, readdirSync, readFileSync, realpathSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { t } from "../i18n.js";
import { parseGoTestJson, parseJestJson, parseJUnit } from "../parsers.js";
import { localBin } from "../exec.js";
import { errorLine, firstLine, relTo, tryJSON } from "../util.js";
import { absDir, exec, fresh, newResult, notePreexisting, preexisting, skip, timedOut, use } from "./common.js";

export const id = "tests";

const NPM_PLACEHOLDER = /no test specified/;
const HOOKS = fileURLToPath(new URL("../hooks/", import.meta.url));
// Runs of one suite on one side (head or base): the first, then one more per hang found.
const MAX_ATTEMPTS = 3;

function slug(dir) {
  return dir ? dir.replace(/[^A-Za-z0-9_.-]+/g, "_") : "root";
}

// pyPrefix is how a Python project runs its tools: through uv or poetry when it uses them.
export function pyPrefix(ctx, p) {
  if (p.manager === "uv" && ctx.toolbox.find("uv")) return ["uv", "run"];
  if (p.manager === "poetry" && ctx.toolbox.find("poetry")) return ["poetry", "run"];
  return [];
}

// testId is how a test is named across head, base and the quarantine: its file relative to the
// project plus its full name, or the name alone when the runner gives no file (Karma) or the name
// already holds it (pytest's node id).
export function testId(file, name) {
  if (!file) return name;
  return `${file}::${name}`;
}

// relToDir makes a path the runner printed relative to the project directory. Runners print real
// paths, and a temporary directory can sit behind a symlink (/var -> /private/var on macOS).
function relToDir(dir, p) {
  if (!p) return "";
  let s = relTo(dir, String(p));
  if (s.startsWith("/")) {
    try {
      s = relTo(realpathSync(dir), s);
    } catch {
      // left absolute
    }
  }
  return s;
}

// readProgress reads the JSON lines a progress hook wrote: the tests that ended (with their
// status) and the ones that started and never ended, leaves only (a suite that is still open
// because a test inside it hangs is not the culprit).
export function readProgress(path, dir) {
  const ends = new Map();
  const started = new Map();
  const errors = [];
  let text = "";
  try {
    text = readFileSync(path, "utf8");
  } catch {
    return { ends, inflight: [], errors, present: false };
  }
  for (const line of text.split("\n")) {
    const ev = tryJSON(line);
    if (!ev || typeof ev !== "object") continue;
    if (ev.event === "error") {
      errors.push(ev.message || "");
      continue;
    }
    const file = ev.nodeid ? "" : relToDir(dir, ev.file || "");
    const name = ev.event === "file-start" || ev.event === "file-end" ? "" : String(ev.name ?? "");
    const tid = testId(file, name);
    if (ev.event === "start" || ev.event === "file-start") started.set(tid, { id: tid, file: ev.nodeid ? relToDir(dir, ev.file || "") : file, name });
    else if (ev.event === "end" || ev.event === "file-end") {
      started.delete(tid);
      if (ev.event === "end" && !ev.suite) {
        const prev = ends.get(tid);
        // pytest reports setup, call and teardown: a failure in any of them is the outcome.
        if (!prev || prev.status !== "fail") ends.set(tid, { id: tid, file: ev.nodeid ? relToDir(dir, ev.file || "") : file, name, status: ev.status, message: ev.message || "", line: ev.line || 0 });
      }
    }
  }
  const open = [...started.values()];
  // A file whose tests started is not itself the hang; neither is a suite with an open test in it.
  const inflight = open.filter((o) => !open.some((x) => x !== o && x.file === o.file && (o.name === "" ? x.name !== "" : x.name.startsWith(o.name + " "))));
  return { ends, inflight, errors, present: true };
}

// escapeJs writes a test name into a JavaScript regular expression with every character outside
// [A-Za-z0-9_] as \uXXXX: no space, quote or slash survives to break NODE_OPTIONS, a shell-free
// argv or karma-jasmine's /pattern/ form.
export function escapeJs(name) {
  let out = "";
  for (const ch of String(name)) {
    if (/[A-Za-z0-9_]/.test(ch)) out += ch;
    else for (const unit of ch.split("")) out += `\\u${unit.charCodeAt(0).toString(16).padStart(4, "0")}`;
  }
  return out;
}

// exclusionPattern matches every full name except the given ones.
export function exclusionPattern(names) {
  return `^(?!(?:${names.map(escapeJs).join("|")})$)`;
}

// escapeRe2 quotes a Go test name for -skip.
function escapeRe2(name) {
  return String(name).replace(/[\\.+*?()|[\]{}^$]/g, "\\$&");
}

// karmaIncomplete reads Karma's console output for a run that did not cover the suite: the last
// "Executed N of M" with N (plus skipped) under M, a browser that disconnected, a browser ERROR
// after the last count, or nothing executed. Karma and `ng test` can exit 0 after a disconnect, so
// the exit code alone would read 18 of 3279 tests as a pass. null when the output has no Karma
// count at all or the run was whole.
export function karmaIncomplete(text) {
  const clean = String(text || "")
    .replace(/\x1b\[[0-9;]*[A-Za-z]/g, "")
    .replace(/\r/g, "\n");
  const counts = [...clean.matchAll(/Executed (\d+) of (\d+)(.*)$/gm)];
  if (!counts.length) return /\bDisconnected\b/.test(clean) ? { reason: "disconnected", executed: 0, total: 0 } : null;
  const last = counts[counts.length - 1];
  const executed = Number(last[1]);
  const total = Number(last[2]);
  const skipped = Number(/\(skipped (\d+)\)/.exec(last[3])?.[1] || 0);
  const after = clean.slice(last.index);
  if (/DISCONNECTED|\bDisconnected\b/.test(after) || /DISCONNECTED/.test(last[3])) return { reason: "disconnected", executed, total };
  if (/\bERROR\b/.test(last[3]) || /^\S.*\bERROR\s*$/m.test(after.slice(last[0].length))) return { reason: "error", executed, total };
  if (!total || !executed) return { reason: "none", executed, total };
  if (executed + skipped < total) return { reason: "partial", executed, total };
  return null;
}

function karmaReason(k) {
  return t(`tests.karma_${k.reason}`, { executed: k.executed, total: k.total });
}

function nodeVersion(text) {
  const m = /v?(\d+)\.(\d+)/.exec(text || "");
  return m ? [Number(m[1]), Number(m[2])] : [0, 0];
}

// Runner adapters. prepare({where, dir, excluded, progress, coverage}) returns the command;
// parse({r, dir, spec, prog}) returns {failures: [{id, name, file, line, message}], generic,
// inflight?}. exclusion: "name" when a single test can be left out, "none" when it cannot (the
// report says so). ids: false when the runner names no tests, so nothing can be compared with base.

function goAdapter(ctx, p) {
  const go = ctx.toolbox.find("go");
  if (!go) return { missing: "go" };
  const pkgRel = (pkg) => (pkg.startsWith(p.module) ? pkg.slice(p.module.length).replace(/^\//, "") : pkg) || ".";
  // -skip arrived in Go 1.20; a module that declares an older go cannot count on it.
  const [maj, min] = (p.goVersion || "1.20").split(".").map(Number);
  return {
    runner: "go",
    tool: "go",
    bin: go,
    exclusion: maj > 1 || (maj === 1 && min >= 20) ? "name" : "none",
    ids: true,
    progress: false,
    prepare({ excluded, coverage, tag }) {
      const args = ["test", "-json", "-count=1"];
      let profile = null;
      if (coverage) {
        profile = join(ctx.out, "coverage", `go-${tag}.out`);
        fresh(profile);
        args.push("-covermode=atomic", `-coverprofile=${profile}`);
      }
      const names = [...new Set(excluded.map((e) => e.name.split("/")[0]))];
      if (names.length) args.push("-skip", `^(?:${names.map(escapeRe2).join("|")})$`);
      args.push("./...");
      return { cmd: "go", args, coverage: profile };
    },
    parse({ r }) {
      const { failedTests, buildFailures } = parseGoTestJson(r.stdout);
      const failures = [];
      for (const ft of failedTests) {
        if (failedTests.some((o) => o !== ft && o.pkg === ft.pkg && o.test.startsWith(ft.test + "/"))) continue;
        const loc = /(\S+_test\.go):(\d+):/.exec(ft.output);
        const pkgDir = pkgRel(ft.pkg) === "." ? "" : pkgRel(ft.pkg);
        const top = ft.test.split("/")[0];
        failures.push({
          id: testId(pkgRel(ft.pkg), top),
          name: top,
          file: loc ? [pkgDir, loc[1]].filter(Boolean).join("/") : "",
          line: loc ? Number(loc[2]) : 0,
          message: `${ft.pkg} ${ft.test}: ${firstLine(ft.output.replace(/^=== RUN.*$/gm, ""))}`,
        });
      }
      const build = buildFailures.map((pkg) => ({ rule: "build-failure", message: t("tests.go_build_failed", { pkg }) }));
      // A run cut short: tests that printed "run" and never passed, failed or skipped.
      const open = new Map();
      for (const raw of r.stdout.split("\n")) {
        const ev = tryJSON(raw);
        if (!ev?.Test) continue;
        const k = `${ev.Package}\0${ev.Test}`;
        if (ev.Action === "run") open.set(k, ev);
        else if (["pass", "fail", "skip"].includes(ev.Action)) open.delete(k);
      }
      const inflight = [];
      for (const ev of open.values()) {
        const top = ev.Test.split("/")[0];
        const tid = testId(pkgRel(ev.Package), top);
        if (!inflight.some((x) => x.id === tid)) inflight.push({ id: tid, name: top, file: "" });
      }
      const generic = r.code !== 0 && !failures.length && !build.length && !r.timedOut ? firstLine(r.stderr || r.stdout) || `go test exit ${r.code}` : null;
      return { failures, build, generic, inflight };
    },
  };
}

// jsonReportParse reads the --json report Jest and Vitest share.
function jsonReportParse(dir) {
  return ({ r, spec, prog }) => {
    const doc = spec.report && existsSync(spec.report) ? tryJSON(readFileSync(spec.report, "utf8")) : undefined;
    const failures = (doc ? parseJestJson(doc, (f) => relToDir(dir, f)) : []).map((f) => ({ id: f.name === f.file ? testId(f.file, "") : testId(f.file, f.name), name: f.name === f.file ? "" : f.name, file: f.file, line: f.line, message: `${f.name}: ${firstLine(f.message)}` }));
    // Killed before the report: what the progress hook saw failing is all there is.
    if (!doc) for (const e of prog.ends.values()) if (e.status === "fail") failures.push({ id: e.id, name: e.name, file: e.file, line: 0, message: `${e.name}: ${e.message}` });
    const generic = r.code !== 0 && !failures.length && !r.timedOut ? firstLine(r.stderr || r.stdout) || `exit ${r.code}` : null;
    // A run that exits without its report, or ran no test at all, verified nothing.
    let incomplete = null;
    if (!r.timedOut && !doc && r.code === 0) incomplete = t("tests.no_report");
    else if (doc && !doc.numTotalTests && !failures.length) incomplete = t("tests.none_ran");
    return { failures, generic, incomplete };
  };
}

function vitestAdapter(ctx, p, dir) {
  const bin = ctx.toolbox.find("vitest", dir);
  if (!bin) return { missing: "vitest" };
  return {
    runner: "vitest",
    tool: "vitest",
    bin,
    exclusion: "name",
    ids: true,
    progress: true,
    prepare({ where, runDir, excluded, progress, coverage, tag }) {
      const report = join(ctx.out, "raw", `test-results-${tag}-${where}.json`);
      fresh(report);
      const args = ["run", "--reporter=json", `--outputFile=${report}`, `--reporter=${join(HOOKS, "vitest-progress.mjs")}`];
      let lcov = null;
      // Vitest writes no coverage when a test fails unless told to; coverage_new still has to see
      // which new lines ran.
      if (coverage && p.coverageProvider) {
        const lcovDir = join(ctx.out, "coverage", `js-${tag}`);
        fresh(lcovDir);
        lcov = join(lcovDir, "lcov.info");
        args.push("--coverage.enabled=true", "--coverage.reporter=lcov", `--coverage.reportsDirectory=${lcovDir}`, "--coverage.reportOnFailure=true");
      }
      const names = excluded.filter((e) => e.name).map((e) => e.name);
      if (names.length) args.push(`--testNamePattern=${exclusionPattern(names)}`);
      for (const e of excluded.filter((x) => !x.name && x.file)) args.push(`--exclude=${e.file}`);
      return { cmd: bin.startsWith(dir) ? bin.replace(dir, runDir) : bin, args, env: { CI: "1", FORCE_COLOR: "0", THULU_GATES_PROGRESS: progress }, report, coverage: lcov };
    },
    parse: jsonReportParse(dir),
  };
}

function jestAdapter(ctx, p, dir) {
  const bin = ctx.toolbox.find("jest", dir);
  if (!bin) return { missing: "jest" };
  return {
    runner: "jest",
    tool: "jest",
    bin,
    exclusion: "name",
    ids: true,
    progress: true,
    prepare({ where, runDir, excluded, progress, coverage, tag }) {
      const report = join(ctx.out, "raw", `test-results-${tag}-${where}.json`);
      fresh(report);
      const args = ["--ci", "--json", `--outputFile=${report}`, "--reporters=default", `--reporters=${join(HOOKS, "jest-progress.cjs")}`];
      let lcov = null;
      if (coverage) {
        const lcovDir = join(ctx.out, "coverage", `js-${tag}`);
        fresh(lcovDir);
        lcov = join(lcovDir, "lcov.info");
        args.push("--coverage", "--coverageReporters=lcov", `--coverageDirectory=${lcovDir}`);
      }
      const names = excluded.filter((e) => e.name).map((e) => e.name);
      if (names.length) args.push(`--testNamePattern=${exclusionPattern(names)}`);
      // A whole file (Jest before 29.6 only says which file hung): a path pattern that matches
      // every test file but those, which keeps the project's own testPathIgnorePatterns.
      const files = excluded.filter((e) => !e.name && e.file).map((e) => e.file);
      if (files.length) args.push(`^(?!.*(?:^|/)(?:${files.map(escapeJs).join("|")})$)`);
      return { cmd: bin.startsWith(dir) ? bin.replace(dir, runDir) : bin, args, env: { CI: "1", FORCE_COLOR: "0", THULU_GATES_PROGRESS: progress }, report, coverage: lcov };
    },
    parse: jsonReportParse(dir),
  };
}

// angularTest finds the project in angular.json with a Karma test target and its karma config.
function angularTest(dir) {
  const doc = tryJSON(readText(join(dir, "angular.json")) || "");
  if (!doc?.projects) return null;
  const withTests = Object.entries(doc.projects).filter(([, pr]) => /karma/.test(String((pr.architect || pr.targets || {}).test?.builder || "")));
  if (withTests.length !== 1) return null;
  const [name, pr] = withTests[0];
  const cfg = (pr.architect || pr.targets).test.options?.karmaConfig;
  return { name, karmaConfig: cfg ? join(dir, cfg) : null };
}

function readText(p) {
  try {
    return readFileSync(p, "utf8");
  } catch {
    return null;
  }
}

// progressParse reads failures from a progress hook that also reports outcomes (Karma, node --test).
function progressParse({ r, prog }) {
  const failures = [];
  for (const e of prog.ends.values()) if (e.status === "fail") failures.push({ id: e.id, name: e.name, file: e.file, line: e.line || 0, message: `${e.name}: ${e.message}` });
  const karma = karmaIncomplete(r.stdout + "\n" + r.stderr);
  let incomplete = karma ? karmaReason(karma) : null;
  // The hook was loaded and saw no test end: nothing ran.
  if (!incomplete && !r.timedOut && prog.present && !prog.ends.size) incomplete = t("tests.none_ran");
  const why = prog.errors[0] || errorLine(r.stdout + "\n" + r.stderr);
  const generic = r.code !== 0 && !failures.length && !r.timedOut && !incomplete ? why || `exit ${r.code}` : null;
  return { failures, generic, incomplete };
}

function karmaAdapter(ctx, p, dir) {
  const angular = Boolean(p.deps["@angular/cli"]) && (!p.scripts.test || /\bng\s+test\b/.test(p.scripts.test));
  // Karma and the Angular CLI are always the project's own (node_modules/.bin), never a global
  // install; they are not in the toolbox catalog because doctor has nothing to suggest for them.
  const ng = angular ? localBin(dir, "ng", ctx.root) : null;
  const karma = localBin(dir, "karma", ctx.root);
  if (angular ? !ng : !karma) return { missing: angular ? "ng" : "karma" };
  const ngProject = angular ? angularTest(dir) : null;
  let config = ngProject?.karmaConfig || null;
  if (!config) for (const n of ["karma.conf.js", "karma.conf.cjs", "karma.config.js", "src/karma.conf.js"]) if (existsSync(join(dir, n))) config = join(dir, n);
  const bin = angular ? ng : karma;
  return {
    runner: "karma",
    tool: angular ? "ng" : "karma",
    bin,
    // Without a config file to wrap there is no reporter and no --grep: the suite still runs
    // once, without watching.
    exclusion: config ? "name" : "none",
    ids: Boolean(config),
    progress: Boolean(config),
    prepare({ where, runDir, excluded, progress, coverage, tag }) {
      const cmd = bin.startsWith(dir) ? bin.replace(dir, runDir) : bin;
      const env = { CI: "1", FORCE_COLOR: "0" };
      if (!config) return { cmd, args: angular ? ["test", "--watch=false", ...(coverage ? ["--code-coverage"] : [])] : ["start", "--single-run"], env };
      let lcov = null;
      if (coverage) {
        const lcovDir = join(ctx.out, "coverage", `js-${tag}`);
        fresh(lcovDir);
        lcov = join(lcovDir, "lcov.info");
        env.THULU_GATES_COVERAGE_DIR = lcovDir;
      }
      Object.assign(env, {
        THULU_GATES_KARMA_CONFIG: config.startsWith(dir) ? config.replace(dir, runDir) : config,
        THULU_GATES_PROGRESS: progress,
        THULU_GATES_GREP: excluded.length ? `/${exclusionPattern(excluded.map((e) => e.name))}/` : "",
      });
      const wrapper = join(HOOKS, "karma.conf.cjs");
      const args = angular ? ["test", ...(ngProject ? [ngProject.name] : []), "--watch=false", `--karma-config=${wrapper}`, ...(coverage ? ["--code-coverage"] : [])] : ["start", wrapper, "--single-run"];
      return { cmd, args, env, coverage: lcov, where };
    },
    parse: progressParse,
  };
}

// nodeTestAdapter runs a `node --test` script through the package manager, with the progress
// reporter and --test-skip-pattern passed in NODE_OPTIONS (the script's command line is the
// project's).
async function nodeTestAdapter(ctx, res, p) {
  const pm = ctx.toolbox.find(p.manager) ? p.manager : "npm";
  const node = ctx.toolbox.find("node");
  const [maj, min] = nodeVersion(node ? await ctx.toolbox.version("node", node) : "");
  // --test-reporter in NODE_OPTIONS from Node 20; --test-skip-pattern from 22.1.
  const canReport = maj >= 20;
  const canSkip = maj > 22 || (maj === 22 && min >= 1);
  return {
    runner: "node",
    tool: pm,
    bin: ctx.toolbox.find(pm),
    exclusion: canSkip ? "name" : "none",
    ids: canReport,
    progress: canReport,
    prepare({ excluded, progress }) {
      const opts = [process.env.NODE_OPTIONS || ""];
      if (canReport) opts.push("--test-reporter=spec", "--test-reporter-destination=stdout", `--test-reporter=${join(HOOKS, "node-progress.mjs")}`, "--test-reporter-destination=stderr");
      // The pattern names the tests to skip; Node matches it against a test's name and its full name.
      if (canSkip && excluded.length) opts.push(`--test-skip-pattern=/^(?:${excluded.map((e) => escapeJs(e.name)).join("|")})$/`);
      return { cmd: pm, args: ["test"], env: { CI: "1", FORCE_COLOR: "0", NODE_OPTIONS: opts.filter(Boolean).join(" "), THULU_GATES_PROGRESS: progress } };
    },
    parse: progressParse,
  };
}

function npmScriptAdapter(ctx, p) {
  const pm = ctx.toolbox.find(p.manager) ? p.manager : "npm";
  // `ng test` watches by default and would never exit.
  const extra = /\bng\s+test\b/.test(p.scripts.test) && !/--watch/.test(p.scripts.test) ? ["--", "--watch=false"] : [];
  return {
    runner: `${pm} test`,
    tool: pm,
    bin: ctx.toolbox.find(pm),
    exclusion: "none",
    ids: false,
    progress: false,
    prepare: () => ({ cmd: pm, args: ["test", ...extra], env: { CI: "1", FORCE_COLOR: "0" } }),
    parse: ({ r }) => {
      // The script may be Karma under another name (`ng test`): its count still tells a partial run.
      const karma = karmaIncomplete(r.stdout + "\n" + r.stderr);
      return { failures: [], incomplete: karma ? karmaReason(karma) : null, generic: r.code !== 0 && !r.timedOut && !karma ? `${pm} test (${p.dir || "."}): ${firstLine(r.stderr || r.stdout)}` : null };
    },
  };
}

function pytestAdapter(ctx, p) {
  const prefix = pyPrefix(ctx, p);
  let cmd;
  if (prefix.length) cmd = [...prefix, "pytest"];
  else if (ctx.toolbox.find("pytest")) cmd = ["pytest"];
  else return { missing: "pytest" };
  return {
    runner: "pytest",
    tool: "pytest",
    bin: ctx.toolbox.find("pytest") || prefix[0],
    exclusion: "name",
    ids: true,
    progress: true,
    async prepare({ where, runDir, excluded, progress, coverage, tag }) {
      const junit = join(ctx.out, `pytest-${tag}${where === "base" ? "-base" : ""}.xml`);
      fresh(junit);
      const args = ["-q", "-p", "no:cacheprovider", "-p", "thulu_gates_progress", `--junitxml=${junit}`];
      // pytest-cov keeps its data file (.coverage, .coverage.* in parallel) in the cwd and Python
      // writes __pycache__ next to the code: both would end up committed with the task.
      const covDir = join(ctx.out, "coverage");
      const data = join(covDir, `py-${tag}${where === "base" ? "-base" : ""}.coverage`);
      fresh(data);
      let xml = null;
      if (coverage) {
        const py = prefix.length ? [...prefix, "python"] : ["python3"];
        const hasCov = (await ctx.run(py[0], [...py.slice(1), "-c", "import pytest_cov"], { cwd: runDir, timeoutMs: 60_000 })).code === 0;
        if (hasCov) {
          xml = join(covDir, `py-${tag}.xml`);
          fresh(xml);
          args.push("--cov=.", `--cov-report=xml:${xml}`);
        }
      }
      for (const e of excluded) args.push(`--deselect=${e.name}`);
      const env = { CI: "1", COVERAGE_FILE: data, PYTHONDONTWRITEBYTECODE: "1", THULU_GATES_PROGRESS: progress, PYTHONPATH: [HOOKS.replace(/\/$/, ""), process.env.PYTHONPATH].filter(Boolean).join(":") };
      return { cmd: cmd[0], args: [...cmd.slice(1), ...args], env, coverage: xml, junit };
    },
    parse({ r, dir, spec, prog }) {
      // 5: no test collected; 2, 3, 4: interrupted, internal error, usage error. None verified anything.
      if (r.code === 5) return { failures: [], generic: null, incomplete: t("tests.none_ran") };
      if (r.code >= 2 && r.code <= 4) return { failures: [], generic: null, incomplete: `pytest exit ${r.code}: ${firstLine(r.stdout || r.stderr)}` };
      let failures = [];
      for (const e of prog.ends.values()) if (e.status === "fail") failures.push({ id: e.id, name: e.name, file: e.file, line: e.line, message: `${e.name}: ${e.message}` });
      if (!failures.length && existsSync(spec.junit)) {
        failures = parseJUnit(readFileSync(spec.junit, "utf8")).map((f) => {
          const file = f.file || junitFile(dir, f.classname, [""], [".py"]);
          const testName = f.classname && f.name.startsWith(f.classname + ".") ? f.name.slice(f.classname.length + 1) : f.name;
          const mod = file.replace(/\.py$/, "").split("/").join(".");
          const cls = f.classname.startsWith(mod + ".") ? f.classname.slice(mod.length + 1).split(".") : [];
          const nodeid = file ? [file, ...cls, testName].join("::") : f.name;
          return { id: nodeid, name: nodeid, file, line: f.line, message: `${f.name}: ${firstLine(f.message)}` };
        });
      }
      const generic = r.code !== 0 && !failures.length && !r.timedOut ? firstLine(r.stdout || r.stderr) || `pytest exit ${r.code}` : null;
      return { failures, generic };
    },
  };
}

function rustAdapter(ctx, p) {
  const cargo = ctx.toolbox.find("cargo");
  if (!cargo) return { missing: "cargo" };
  const llvmCov = ctx.toolbox.find("cargo-llvm-cov");
  return {
    runner: "cargo",
    tool: "cargo",
    bin: cargo,
    exclusion: "name",
    ids: true,
    progress: false,
    prepare({ excluded, coverage, tag }) {
      let args;
      let lcov = null;
      if (coverage && llvmCov) {
        lcov = join(ctx.out, "coverage", `rust-${tag}.info`);
        fresh(lcov);
        // --ignore-run-fail: without it a failing test leaves no report and coverage_new skips.
        // With it a failing test exits 0, so failures come from the "test ... FAILED" lines; a
        // build error still exits non-zero.
        args = ["llvm-cov", "--lcov", "--output-path", lcov, "--ignore-run-fail"];
      } else args = ["test"];
      if (excluded.length) args.push("--", "--exact", ...excluded.flatMap((e) => ["--skip", e.name]));
      return { cmd: "cargo", args, coverage: lcov };
    },
    parse({ r }) {
      const text = r.stdout + "\n" + r.stderr;
      const failures = parseCargoTest(text).map((f) => ({ id: f.name, name: f.name, file: f.file, line: f.line, message: f.message ? `${f.name}: ${f.message}` : `${f.name} FAILED` }));
      // libtest names a test that runs long; one still running when the run was cut is the hang.
      const done = new Set([...text.matchAll(/^test (\S+) \.\.\. \w+/gm)].map((m) => m[1]));
      const inflight = [...new Set([...text.matchAll(/^test (\S+) has been running for over \d+ seconds$/gm)].map((m) => m[1]))].filter((n) => !done.has(n)).map((n) => ({ id: n, name: n, file: "" }));
      const generic = r.code !== 0 && !failures.length && !r.timedOut ? errorLine(r.stderr) || `cargo exit ${r.code}` : null;
      return { failures, generic, inflight };
    },
  };
}

function javaAdapter(ctx, p, dir) {
  const tool = p.manager === "maven" ? "mvn" : "gradle";
  const bin = p.wrapper ? p.wrapper : ctx.toolbox.find(tool) ? tool : null;
  if (!bin) return { missing: tool };
  const maven = p.manager === "maven";
  return {
    runner: tool,
    tool,
    bin: p.wrapper ? join(dir, p.wrapper) : ctx.toolbox.find(tool),
    exclusion: "none",
    ids: true,
    progress: false,
    prepare({ runDir, coverage }) {
      const reportDirs = [maven ? join(runDir, "target", "surefire-reports") : join(runDir, "build", "test-results", "test")];
      const jx = maven ? join(runDir, "target", "site", "jacoco", "jacoco.xml") : join(runDir, "build", "reports", "jacoco", "test", "jacocoTestReport.xml");
      // Both live in the build directory across runs: a build that breaks before writing them
      // must not hand back the last run's failures or coverage.
      fresh(...reportDirs, jx);
      // A failing test stops Maven before jacoco:report; with failures ignored the build goes on
      // and Surefire's XML still carries them. A compile error still fails the build.
      const jacoco = coverage && p.jacoco;
      const goals = maven ? (jacoco ? ["-B", "-Dmaven.test.failure.ignore=true", "test", "jacoco:report"] : ["-B", "-Dmaven.test.failure.ignore=true", "test"]) : ["test", ...(jacoco ? ["jacocoTestReport"] : [])];
      return { cmd: bin, args: goals, coverage: jacoco ? jx : null, reportDirs };
    },
    parse({ r, dir, spec }) {
      let raw = [];
      for (const d of spec.reportDirs) {
        if (!existsSync(d)) continue;
        for (const f of readdirSync(d).filter((n) => n.endsWith(".xml"))) raw = raw.concat(parseJUnit(readFileSync(join(d, f), "utf8")));
      }
      const failures = raw.map((f) => {
        const file = f.file || junitFile(dir, f.classname, ["src/test/java", "src/test/kotlin"], [".java", ".kt"]);
        const method = f.classname && f.name.startsWith(f.classname + ".") ? f.name.slice(f.classname.length + 1) : f.name;
        return { id: `${f.classname}#${method}`, name: `${f.classname}#${method}`, file, line: f.line, message: `${f.name}: ${firstLine(f.message)}` };
      });
      const generic = r.code !== 0 && !failures.length && !r.timedOut ? errorLine(r.stdout + "\n" + r.stderr) || `${tool} exit ${r.code}` : null;
      return { failures, generic };
    },
  };
}

async function adapterFor(ctx, res, p, dir) {
  if (p.stack === "go") return goAdapter(ctx, p);
  if (p.stack === "python") return pytestAdapter(ctx, p);
  if (p.stack === "rust") return rustAdapter(ctx, p);
  if (p.stack === "java") return javaAdapter(ctx, p, dir);
  if (p.stack === "ts" || p.stack === "js") {
    if (p.testRunner === "vitest") return vitestAdapter(ctx, p, dir);
    if (p.testRunner === "jest") return jestAdapter(ctx, p, dir);
    if (p.deps.karma) return karmaAdapter(ctx, p, dir);
    if (p.scripts.test && !NPM_PLACEHOLDER.test(p.scripts.test)) {
      if (/(?:^|[\s;&|])node\b[^;&|]*\s--test\b/.test(p.scripts.test)) return nodeTestAdapter(ctx, res, p);
      return npmScriptAdapter(ctx, p);
    }
  }
  return {};
}

// runSuite runs a project's suite on one side. When the run is cut (time budget or no progress
// for timeouts.test_stall) and the runner can say which tests never finished, it runs again
// without them, up to MAX_ATTEMPTS; those tests come back in hung.
async function runSuite(ctx, res, ad, { where, dir, runDir, excluded, tag }) {
  const hung = [];
  let ex = [...excluded];
  const stall = ctx.cfg.timeouts?.test_stall ?? 300;
  for (let attempt = 1; ; attempt++) {
    const progress = join(ctx.out, "raw", `tests-progress-${tag}-${where}-${attempt}.jsonl`);
    fresh(progress);
    const spec = await ad.prepare({ where, dir, runDir, excluded: ex, progress, coverage: where === "head", tag });
    const r = await exec(ctx, res, spec.cmd, spec.args, { cwd: runDir, env: spec.env, stallMs: stall > 0 ? stall * 1000 : 0, watchFile: ad.progress ? progress : undefined });
    const prog = readProgress(progress, runDir);
    const out = ad.parse({ r, dir: runDir, spec, prog });
    const result = { ...out, hung, spec, timedOut: r.timedOut, stalled: Boolean(r.stalled), completed: !r.timedOut && !out.incomplete };
    if (result.completed) return result;
    const inflight = (out.inflight || prog.inflight).filter((x) => !ex.some((e) => e.id === x.id));
    hung.push(...inflight);
    if (!inflight.length || ad.exclusion === "none" || attempt >= MAX_ATTEMPTS) return result;
    ex = [...ex, ...inflight];
  }
}

// baseSuite runs the same suite on the base checkout, once per base commit, runner, version and
// set of excluded tests. It answers which tests fail and which hang there.
async function baseSuite(ctx, res, p, ad, excluded, tag) {
  const version = res._tools.find((x) => x.name === ad.tool)?.version || "";
  const parts = ["tests", p.dir, ad.runner, version, excluded.map((e) => e.id).sort()];
  const { value, cached } = await ctx.cache.memo(parts, async () => {
    const runDir = await ctx.baseProjectDir(p);
    if (!runDir) return { value: undefined };
    const out = await runSuite(ctx, res, ad, { where: "base", dir: absDir(ctx, p.dir), runDir, excluded, tag });
    const value = { failed: out.failures.map((f) => f.id), hung: out.hung.map((h) => h.id), completed: out.completed, incomplete: out.incomplete || null, generic: out.generic || null };
    // A base run that did not cover the suite (cut, disconnected, nothing ran) or broke before
    // naming a test (missing packages in the checkout) is no baseline; it is not kept either, so
    // the next run tries again.
    return { value, cache: out.completed && !(out.generic && !out.failures.length) };
  });
  if (value && cached) res.metrics.base_cached = true;
  return value;
}

function finding(p, f, extra) {
  return { rule: "test-failure", severity: "high", line: 0, ...f, file: f.file ? [p.dir, f.file].filter(Boolean).join("/") : "", blocking: true, ...extra };
}

// runProject runs one project's suite at head, compares what broke with base and fills the
// findings, the quarantine and the metrics. It returns {ran, timedOut, missing}.
async function runProject(ctx, res, p, ad, tag) {
  const dir = absDir(ctx, p.dir);
  await use(ctx, res, ad.tool, ad.bin);
  const quarantined = ctx.quarantine.forProject(p.dir, ad.runner);
  const exclude = ad.exclusion === "none" ? [] : quarantined;
  if (quarantined.length) {
    if (ad.exclusion === "none") res.metrics.quarantine_unsupported = [...(res.metrics.quarantine_unsupported || []), { project: p.dir || ".", runner: ad.runner, tests: quarantined.map((e) => e.id) }];
    else
      res.metrics.quarantined = [
        ...(res.metrics.quarantined || []),
        ...quarantined.map((e) => ({ id: e.id, project: p.dir || ".", reason: e.reason, changed: Boolean(e.file && ctx.diff.has([p.dir, e.file].filter(Boolean).join("/"))) })),
      ];
  }
  const head = await runSuite(ctx, res, ad, { where: "head", dir, runDir: dir, excluded: exclude, tag });
  if (head.spec.coverage && existsSync(head.spec.coverage)) ctx.coverage.push({ path: head.spec.coverage, project: p });
  const suspects = head.failures.length + head.hung.length;
  const ranBase = suspects && ad.ids ? await baseSuite(ctx, res, p, ad, exclude, tag) : null;
  // Only a base run that covered the suite is a baseline; with a partial one every failure counts.
  const base = ranBase?.completed ? ranBase : null;
  if (ranBase && !base) res.metrics.base_incomplete = ranBase.incomplete || t("tests.base_incomplete");
  const baseFailed = new Set(base?.failed || []);
  const baseHung = new Set(base?.hung || []);
  const seconds = Math.round(ctx.timeoutMs(id) / 1000);
  const where = base ? "" : ranBase ? t("tests.base_incomplete") : ad.ids ? t("tests.base_unavailable") : "";
  for (const f of head.failures) {
    if (baseFailed.has(f.id) || baseHung.has(f.id)) {
      res.findings.push(preexisting(finding(p, f, { rule: "test-failure-on-base", message: `${f.message} (${t("tests.fails_on_base")})` })));
      ctx.quarantine.add({ id: f.id, project: p.dir, runner: ad.runner, file: f.file, name: f.name, reason: baseHung.has(f.id) ? "hangs" : "fails", base: ctx.base });
    } else res.findings.push(finding(p, f, where ? { message: `${f.message} (${where})` } : {}));
  }
  for (const h of head.hung) {
    if (baseHung.has(h.id) || baseFailed.has(h.id)) {
      res.findings.push(preexisting(finding(p, h, { rule: "test-hang-on-base", message: t("tests.hangs_on_base", { name: h.name || h.file }) })));
      ctx.quarantine.add({ id: h.id, project: p.dir, runner: ad.runner, file: h.file, name: h.name, reason: "hangs", base: ctx.base });
    } else res.findings.push(finding(p, h, { rule: "test-hang", message: t("tests.hangs_head", { name: h.name || h.file, where: where || t("tests.passes_on_base") }) }));
  }
  for (const b of head.build || []) res.findings.push(finding(p, b));
  if (head.generic) res.findings.push(finding(p, { message: head.generic }));
  if (!head.completed) {
    res.metrics.stalled = res.metrics.stalled || head.stalled;
    if (head.incomplete && !head.timedOut) res.metrics.incomplete = [...(res.metrics.incomplete || []), `${p.dir || "."}: ${head.incomplete}`];
    return { ran: true, timedOut: true, seconds, reason: head.timedOut ? null : head.incomplete, unexcludable: ad.exclusion === "none" && head.hung.length > 0 };
  }
  return { ran: true };
}

export async function run(ctx) {
  const res = newResult(id);
  const covDir = join(ctx.out, "coverage");
  mkdirSync(covDir, { recursive: true });

  if (ctx.cfg.commands?.tests) {
    const r = await exec(ctx, res, "sh", ["-c", ctx.cfg.commands.tests], { env: { CI: "1", THULU_GATES_QUARANTINE: ctx.quarantine.path } });
    if (r.timedOut) return timedOut(res, ctx.timeoutMs(id) / 1000, false);
    for (const f of ctx.cfg.coverage_files || []) ctx.coverage.push({ path: join(ctx.root, f), project: null });
    if (r.code !== 0) {
      res.findings.push({ rule: "test-failure", severity: "high", file: "", line: 0, message: firstLine(r.stderr || r.stdout), blocking: true });
    }
    return finish(ctx, res, false);
  }

  const projects = ctx.projects;
  if (!projects.length) return skip(res, t("tests.no_projects"));
  const missing = [];
  let ran = 0;
  let cut = null;
  for (const p of projects) {
    const dir = absDir(ctx, p.dir);
    const ad = await adapterFor(ctx, res, p, dir);
    if (ad.missing) missing.push(`${ad.missing} (${p.dir || "."})`);
    if (!ad.runner) continue;
    const outcome = await runProject(ctx, res, p, ad, slug(p.dir));
    if (outcome.ran) ran++;
    if (outcome.timedOut) cut = outcome;
  }
  res.metrics.projects_run = ran;
  if (!ran) return skip(res, missing.length ? t("tests.nothing_ran_missing", { what: missing.join(", ") }) : t("tests.no_runner"));
  if (missing.length) res.metrics.not_run = missing;
  return finish(ctx, res, cut);
}

function finish(ctx, res, cut) {
  const failures = res.findings.filter((f) => f.blocking).length;
  if (failures) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("tests.failed", { count: failures });
  } else if (cut) {
    // Not verified: the suite did not finish (cut, or the runner stopped short) and the tests that
    // stopped it could not be left out.
    timedOut(res, cut.seconds, false);
    if (cut.reason) {
      delete res.metrics.timed_out;
      res.summary = t("tests.incomplete", { reason: res.metrics.incomplete.join("; ") });
    }
    if (cut.unexcludable) res.summary = `${res.summary}; ${t("tests.cannot_exclude")}`;
  } else {
    res.status = res.findings.length ? "warn" : "pass";
    res.summary = res.metrics.not_run ? t("tests.passed_partial", { missing: res.metrics.not_run.join(", ") }) : t("tests.passed");
  }
  if (cut && failures) {
    if (!cut.reason) res.metrics.timed_out = true;
    res.metrics.not_verified = true;
    res.summary = `${res.summary}; ${cut.reason ? t("tests.incomplete", { reason: res.metrics.incomplete.join("; ") }) : t("tests.rest_not_verified", { seconds: cut.seconds })}`;
  }
  const q = res.metrics.quarantined || [];
  if (q.length) res.summary = `${res.summary}; ${t("tests.quarantined", { count: q.length })}`;
  const unsupported = res.metrics.quarantine_unsupported || [];
  if (unsupported.length) res.summary = `${res.summary}; ${t("tests.quarantine_unsupported", { runners: unsupported.map((u) => u.runner).join(", ") })}`;
  if (ctx.quarantine.added.length) res.metrics.quarantine_added = ctx.quarantine.added.map((e) => e.id);
  return notePreexisting(res);
}

// parseCargoTest reads libtest's output: each "test x ... FAILED" line, with the file, line and
// first message line of its panic ("thread 'x' (123) panicked at src/a.rs:43:9:\nassertion ...").
export function parseCargoTest(text) {
  const panics = new Map();
  for (const m of text.matchAll(/^thread '([^']+)'(?: \(\d+\))? panicked at ([^:\n]+):(\d+):\d+:\n(.*)$/gm)) panics.set(m[1], { file: m[2], line: Number(m[3]), message: m[4].trim() });
  return [...text.matchAll(/^test (\S+) \.\.\. FAILED$/gm)].map((m) => ({ name: m[1], file: "", line: 0, message: "", ...panics.get(m[1]) }));
}

// junitFile finds the test file of a JUnit testcase that names no file (pytest's xunit2 and
// Surefire only give the classname): tests.test_stats -> tests/test_stats.py,
// com.example.FooTest -> src/test/java/com/example/FooTest.java. Empty when no such file exists.
export function junitFile(dir, classname, roots, exts) {
  if (!classname) return "";
  const parts = classname.split(".");
  // pytest adds the class to the module path for methods of a test class.
  for (let n = parts.length; n > 0; n--) {
    const stem = parts.slice(0, n).join("/").replace(/\$.*$/, "");
    for (const root of roots) {
      for (const ext of exts) {
        const rel = [root, stem + ext].filter(Boolean).join("/");
        if (existsSync(join(dir, rel))) return rel;
      }
    }
  }
  return "";
}
