// Stack detection: which projects (a language plus the directory holding its manifest) the diff
// touches, how each one builds, and which commands the repo already defines.

import { existsSync, readdirSync, readFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";

import { langOf, tryJSON } from "./util.js";

const PY_MARKERS = ["pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"];

// Manifests that make a change count for a project even though they are not source files.
const MANIFESTS = new Set([
  "go.mod",
  "go.sum",
  "go.work",
  "package.json",
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "bun.lock",
  "tsconfig.json",
  "pyproject.toml",
  "setup.py",
  "setup.cfg",
  "uv.lock",
  "poetry.lock",
  "Cargo.toml",
  "Cargo.lock",
  "pom.xml",
  "build.gradle",
  "build.gradle.kts",
  "settings.gradle",
  "settings.gradle.kts",
]);

function isManifest(path) {
  const b = basename(path);
  return MANIFESTS.has(b) || /^requirements.*\.txt$/.test(b);
}

function has(root, dir, name) {
  return existsSync(join(root, dir, name));
}

function markersIn(root, dir) {
  const found = [];
  if (has(root, dir, "go.mod")) found.push("go");
  if (has(root, dir, "package.json")) found.push(has(root, dir, "tsconfig.json") ? "ts" : "js");
  if (PY_MARKERS.some((m) => has(root, dir, m)) || hasRequirements(root, dir)) found.push("python");
  if (has(root, dir, "Cargo.toml")) found.push("rust");
  if (has(root, dir, "pom.xml") || has(root, dir, "build.gradle") || has(root, dir, "build.gradle.kts")) found.push("java");
  return found;
}

function hasRequirements(root, dir) {
  try {
    return readdirSync(join(root, dir)).some((n) => /^requirements.*\.txt$/.test(n));
  } catch {
    return false;
  }
}

// The stack a file's language belongs to; js and ts share package.json.
function stackFor(lang) {
  switch (lang) {
    case "go":
      return ["go"];
    case "js":
    case "ts":
      return ["ts", "js"];
    case "python":
      return ["python"];
    case "rust":
      return ["rust"];
    case "java":
    case "kotlin":
      return ["java"];
    default:
      return null;
  }
}

function manifestStack(name) {
  if (name.startsWith("go.")) return ["go"];
  if (/package|pnpm|yarn|bun|tsconfig/.test(name)) return ["ts", "js"];
  if (/py|setup|uv\.lock|poetry|requirements/.test(name)) return ["python"];
  if (name.startsWith("Cargo")) return ["rust"];
  return ["java"];
}

// detectProjects maps every changed file to the nearest directory, walking up to the repo root,
// whose manifest matches the file's language. A file no project claims (docs, CI yaml) maps to
// none.
export function detectProjects(root, changedPaths) {
  const byKey = new Map();
  for (const p of changedPaths) {
    const wanted = isManifest(p) ? manifestStack(basename(p)) : stackFor(langOf(p));
    if (!wanted) continue;
    let dir = dirname(p) === "." ? "" : dirname(p);
    for (;;) {
      const found = markersIn(root, dir).filter((s) => wanted.includes(s));
      if (found.length) {
        const stack = found[0];
        const key = `${stack}:${dir}`;
        if (!byKey.has(key)) byKey.set(key, describe(root, dir, stack));
        byKey.get(key).files.push(p);
        break;
      }
      if (dir === "") break;
      dir = dirname(dir) === "." ? "" : dirname(dir);
    }
  }
  return [...byKey.values()].sort((a, b) => a.dir.localeCompare(b.dir) || a.stack.localeCompare(b.stack));
}

function lockfileUp(root, dir, names) {
  let d = dir;
  for (;;) {
    for (const n of names) if (has(root, d, n)) return n;
    if (d === "") return null;
    d = dirname(d) === "." ? "" : dirname(d);
  }
}

function describe(root, dir, stack) {
  const p = { stack, dir, files: [] };
  if (stack === "ts" || stack === "js") {
    const pkg = tryJSON(readFileSync(join(root, dir, "package.json"), "utf8")) || {};
    const lock = lockfileUp(root, dir, ["pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "package-lock.json"]);
    p.manager = { "pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "bun.lock": "bun", "bun.lockb": "bun" }[lock] || "npm";
    p.scripts = pkg.scripts || {};
    p.deps = { ...pkg.dependencies, ...pkg.devDependencies, ...pkg.optionalDependencies };
    p.testRunner = p.deps.vitest ? "vitest" : p.deps.jest ? "jest" : null;
    if (!p.testRunner && /\bvitest\b/.test(p.scripts.test || "")) p.testRunner = "vitest";
    if (!p.testRunner && /\bjest\b/.test(p.scripts.test || "")) p.testRunner = "jest";
    p.coverageProvider = Object.keys(p.deps).find((d) => /^@vitest\/coverage-/.test(d)) || null;
    const found = (names) => names.some((n) => has(root, dir, n) || has(root, "", n));
    const flat = found(["eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts"]);
    const legacy = found([".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml"]) || Boolean(pkg.eslintConfig);
    p.eslintConfig = flat || legacy;
    // Only an .eslintrc: eslint 9 reads it with ESLINT_USE_FLAT_CONFIG=false, and refuses to run without it.
    p.eslintLegacy = legacy && !flat;
    p.prettierConfig =
      [".prettierrc", ".prettierrc.json", ".prettierrc.js", ".prettierrc.cjs", ".prettierrc.mjs", ".prettierrc.yaml", ".prettierrc.yml", "prettier.config.js", "prettier.config.mjs", "prettier.config.cjs"].some(
        (n) => has(root, dir, n),
      ) || Boolean(pkg.prettier);
    p.stryker = Boolean(p.deps["@stryker-mutator/core"]);
    p.tsconfig = stack === "ts" ? join(dir, "tsconfig.json") : null;
  } else if (stack === "python") {
    p.manager = has(root, dir, "uv.lock") ? "uv" : has(root, dir, "poetry.lock") ? "poetry" : "pip";
    const pyproject = has(root, dir, "pyproject.toml") ? readFileSync(join(root, dir, "pyproject.toml"), "utf8") : "";
    p.mypyConfig = /^\[tool\.mypy\]/m.test(pyproject) || has(root, dir, "mypy.ini") || has(root, dir, ".mypy.ini");
    p.pyrightConfig = /^\[tool\.pyright\]/m.test(pyproject) || has(root, dir, "pyrightconfig.json");
    p.mutmutConfig = /^\[tool\.mutmut\]/m.test(pyproject) || /^\[mutmut\]/m.test(readSafe(join(root, dir, "setup.cfg")));
  } else if (stack === "java") {
    p.manager = has(root, dir, "pom.xml") ? "maven" : "gradle";
    p.wrapper = p.manager === "maven" ? (has(root, dir, "mvnw") ? "./mvnw" : null) : has(root, dir, "gradlew") ? "./gradlew" : null;
    const build = readSafe(join(root, dir, "pom.xml")) + readSafe(join(root, dir, "build.gradle")) + readSafe(join(root, dir, "build.gradle.kts"));
    p.jacoco = /jacoco/.test(build);
    p.pitest = /pitest/.test(build);
    p.checkstyle = /checkstyle/.test(build);
    p.spotbugs = /spotbugs/.test(build);
  } else if (stack === "go") {
    const mod = readSafe(join(root, dir, "go.mod"));
    p.module = /^module\s+(\S+)/m.exec(mod)?.[1] || "";
    p.goVersion = /^go\s+(\S+)/m.exec(mod)?.[1] || "";
  }
  return p;
}

function readSafe(p) {
  try {
    return readFileSync(p, "utf8");
  } catch {
    return "";
  }
}

// repoCommands lists what the repo already defines for checking itself:
// make/just targets and package.json scripts named like a gate.
export function repoCommands(root) {
  const out = [];
  const make = readSafe(join(root, "Makefile"));
  for (const m of make.matchAll(/^([A-Za-z0-9_-]+):(?!=)/gm)) {
    if (/^(check|check-ci|lint|test|ci|verify|typecheck)$/.test(m[1])) out.push(`make ${m[1]}`);
  }
  const just = readSafe(join(root, "justfile")) || readSafe(join(root, "Justfile"));
  for (const m of just.matchAll(/^([A-Za-z0-9_-]+):/gm)) {
    if (/^(check|lint|test|ci|verify)$/.test(m[1])) out.push(`just ${m[1]}`);
  }
  const pkg = tryJSON(readSafe(join(root, "package.json")) || "{}") || {};
  for (const s of Object.keys(pkg.scripts || {})) {
    if (/^(test|lint|typecheck|check|ci|verify)$/.test(s)) out.push(`npm run ${s}`);
  }
  return [...new Set(out)];
}

// declaredVersions reads the toolchain pins a repo carries.
export function declaredVersions(root) {
  const v = {};
  const tv = readSafe(join(root, ".tool-versions"));
  for (const line of tv.split("\n")) {
    const [name, ver] = line.trim().split(/\s+/);
    if (name && ver && !name.startsWith("#")) v[name] = ver;
  }
  for (const f of ["mise.toml", ".mise.toml"]) {
    const s = readSafe(join(root, f));
    const tools = /\[tools\]([\s\S]*?)(?:\n\[|$)/.exec(s)?.[1] || "";
    for (const m of tools.matchAll(/^\s*"?([\w:./@-]+)"?\s*=\s*"([^"]+)"/gm)) v[m[1]] = m[2];
  }
  const nvm = readSafe(join(root, ".nvmrc")).trim();
  if (nvm) v.node = v.node || nvm;
  const py = readSafe(join(root, ".python-version")).trim();
  if (py) v.python = v.python || py;
  const go = /^go\s+(\S+)/m.exec(readSafe(join(root, "go.mod")))?.[1];
  if (go) v.go = v.go || go;
  return v;
}
