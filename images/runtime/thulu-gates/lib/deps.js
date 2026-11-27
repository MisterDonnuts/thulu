// Direct dependencies a diff adds or bumps, read from the manifests at base and at head. The deps,
// licenses and phantom_deps checks share this list.

import { basename, join } from "node:path";

import { showFile } from "./git.js";
import { readText, tryJSON } from "./util.js";

const NPM_SECTIONS = ["dependencies", "devDependencies", "optionalDependencies", "peerDependencies"];

export function parseNpm(text) {
  const doc = tryJSON(text || "{}") || {};
  const out = new Map();
  for (const s of NPM_SECTIONS) {
    for (const [name, spec] of Object.entries(doc[s] || {})) {
      if (typeof spec !== "string") continue;
      // workspace:, file:, link:, git and URL specs do not come from the registry.
      if (/^(workspace:|file:|link:|portal:|git|https?:|github:)/.test(spec) || spec.includes("/") && !spec.startsWith("npm:")) continue;
      const dep = { name, version: spec, dev: s !== "dependencies" && s !== "optionalDependencies" };
      // "alias": "npm:real-name@^1.2" installs real-name under another name; the registry only
      // knows the real one.
      if (spec.startsWith("npm:")) {
        const target = spec.slice(4);
        const at = target.lastIndexOf("@");
        dep.registryName = at > 0 ? target.slice(0, at) : target;
        dep.version = at > 0 ? target.slice(at + 1) || "*" : "*";
      }
      out.set(name, dep);
    }
  }
  return out;
}

export function parseGoMod(text) {
  const out = new Map();
  const replaced = new Set();
  let inRequire = false;
  let inReplace = false;
  for (const raw of (text || "").split("\n")) {
    const line = raw.replace(/\/\/.*$/, (c) => (/indirect/.test(c) ? " //indirect" : "")).trim();
    if (/^require\s*\($/.test(line)) {
      inRequire = true;
      continue;
    }
    if (/^replace\s*\($/.test(line)) {
      inReplace = true;
      continue;
    }
    if (line === ")") {
      inRequire = inReplace = false;
      continue;
    }
    let m;
    if (inReplace || (m = /^replace\s+(.+)$/.exec(line))) {
      const body = inReplace ? line : m[1];
      const [left, right] = body.split("=>").map((s) => s?.trim());
      if (left && right && /^(\.|\/)/.test(right)) replaced.add(left.split(/\s+/)[0]);
      continue;
    }
    const body = inRequire ? line : /^require\s+(.+)$/.exec(line)?.[1];
    if (!body) continue;
    const parts = body.split(/\s+/);
    if (parts.length < 2) continue;
    out.set(parts[0], { name: parts[0], version: parts[1], indirect: /\/\/indirect/.test(body) });
  }
  for (const r of replaced) out.delete(r);
  return out;
}

function normPy(name) {
  return name.toLowerCase().replace(/[-_.]+/g, "-");
}

function pyReq(s) {
  const m = /^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*(.*)$/.exec(s);
  if (!m) return null;
  const spec = m[3].split(";")[0].trim();
  if (/^@/.test(spec)) return null;
  return { name: normPy(m[1]), version: spec || "*" };
}

export function parseRequirements(text) {
  const out = new Map();
  for (const raw of (text || "").split("\n")) {
    const line = raw.replace(/\s#.*$/, "").trim();
    if (!line || line.startsWith("#") || line.startsWith("-") || /:\/\//.test(line)) continue;
    const r = pyReq(line);
    if (r) out.set(r.name, r);
  }
  return out;
}

// parsePyproject reads PEP 621 dependencies and optional-dependencies arrays, and Poetry tables.
export function parsePyproject(text) {
  const out = new Map();
  const s = text || "";
  for (const m of s.matchAll(/^(?:dependencies|[\w-]+)\s*=\s*\[([\s\S]*?)\]\s*$/gm)) {
    const header = s.slice(0, m.index).match(/^\[([^\]]+)\]\s*$/gm)?.pop() || "";
    if (!/\[project(\.optional-dependencies)?\]|\[dependency-groups\]/.test(header)) continue;
    if (header === "[project]" && !m[0].startsWith("dependencies")) continue;
    for (const q of m[1].matchAll(/["']([^"']+)["']/g)) {
      const r = pyReq(q[1]);
      if (r) out.set(r.name, r);
    }
  }
  for (const m of s.matchAll(/^\[tool\.poetry\.(?:group\.[\w-]+\.)?(?:dev-)?dependencies\]\s*$([\s\S]*?)(?=^\[|$(?![\s\S]))/gm)) {
    for (const l of m[1].matchAll(/^\s*([A-Za-z0-9][\w.-]*)\s*=\s*(?:"([^"]*)"|\{([^}]*)\})/gm)) {
      if (l[1].toLowerCase() === "python") continue;
      if (l[3] && /\b(path|git|url)\s*=/.test(l[3])) continue;
      const version = l[2] ?? /version\s*=\s*"([^"]*)"/.exec(l[3] || "")?.[1] ?? "*";
      out.set(normPy(l[1]), { name: normPy(l[1]), version });
    }
  }
  return out;
}

export function parseCargo(text) {
  const out = new Map();
  const s = text || "";
  for (const m of s.matchAll(/^\[((?:workspace\.)?(?:dev-|build-)?dependencies)\]\s*$([\s\S]*?)(?=^\[|$(?![\s\S]))/gm)) {
    for (const l of m[2].matchAll(/^\s*([A-Za-z0-9_-]+)\s*=\s*(?:"([^"]*)"|\{([^}]*)\})/gm)) {
      if (l[3] && /\b(path|git)\s*=/.test(l[3]) && !/\bversion\s*=/.test(l[3])) continue;
      if (l[3] && /workspace\s*=\s*true/.test(l[3])) continue;
      const version = l[2] ?? /version\s*=\s*"([^"]*)"/.exec(l[3] || "")?.[1] ?? "*";
      const name = /package\s*=\s*"([^"]+)"/.exec(l[3] || "")?.[1] || l[1];
      out.set(name, { name, version, dev: /dev-/.test(m[1]) });
    }
  }
  return out;
}

export function parsePom(text) {
  const out = new Map();
  const s = (text || "").replace(/<!--[\s\S]*?-->/g, "");
  for (const m of s.matchAll(/<dependency>([\s\S]*?)<\/dependency>/g)) {
    const g = /<groupId>\s*([^<\s]+)\s*<\/groupId>/.exec(m[1])?.[1];
    const a = /<artifactId>\s*([^<\s]+)\s*<\/artifactId>/.exec(m[1])?.[1];
    const v = /<version>\s*([^<\s]+)\s*<\/version>/.exec(m[1])?.[1] || "*";
    if (g && a) out.set(`${g}:${a}`, { name: `${g}:${a}`, version: v });
  }
  return out;
}

export function parseGradle(text) {
  const out = new Map();
  for (const m of (text || "").matchAll(/\b(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|kapt|ksp|annotationProcessor)\s*\(?\s*["']([\w.-]+):([\w.-]+):([^"'\s]+)["']/g)) {
    out.set(`${m[1]}:${m[2]}`, { name: `${m[1]}:${m[2]}`, version: m[3] });
  }
  return out;
}

const MANIFESTS = [
  { test: (b) => b === "package.json", ecosystem: "npm", parse: parseNpm },
  { test: (b) => b === "go.mod", ecosystem: "go", parse: parseGoMod },
  { test: (b) => /^requirements.*\.txt$/.test(b), ecosystem: "pypi", parse: parseRequirements },
  { test: (b) => b === "pyproject.toml", ecosystem: "pypi", parse: parsePyproject },
  { test: (b) => b === "Cargo.toml", ecosystem: "cargo", parse: parseCargo },
  { test: (b) => b === "pom.xml", ecosystem: "maven", parse: parsePom },
  { test: (b) => b === "build.gradle" || b === "build.gradle.kts", ecosystem: "maven", parse: parseGradle },
];

function lineOf(text, name) {
  const lines = (text || "").split("\n");
  const short = name.includes(":") ? name.split(":")[1] : name;
  const i = lines.findIndex((l) => l.includes(`"${name}"`) || l.includes(name) || l.includes(short));
  return i >= 0 ? i + 1 : 0;
}

// changedDeps returns [{ecosystem, name, version, oldVersion, file, line, dev, indirect}] for
// every dependency that is new at head or whose version spec changed.
export async function changedDeps(ctx) {
  if (ctx._changedDeps) return ctx._changedDeps;
  const out = [];
  for (const [file, d] of ctx.diff) {
    const m = MANIFESTS.find((x) => x.test(basename(file)));
    if (!m) continue;
    const headText = readText(join(ctx.root, file));
    const baseText = await showFile(ctx.run, ctx.root, ctx.base, d.oldPath || file);
    const before = m.parse(baseText);
    for (const [name, dep] of m.parse(headText)) {
      const old = before.get(name);
      if (old && old.version === dep.version) continue;
      out.push({ ecosystem: m.ecosystem, ...dep, oldVersion: old?.version || null, file, line: lineOf(headText, name) });
    }
  }
  ctx._changedDeps = out;
  return out;
}

// exactVersion strips range operators when the spec pins one version; null for ranges.
export function exactVersion(eco, spec) {
  if (!spec || spec === "*") return null;
  if (eco === "go") return spec;
  if (eco === "pypi") return /^===?\s*([\w.+!-]+)$/.exec(spec)?.[1] || null;
  if (eco === "npm") return /^=?v?(\d+\.\d+\.\d+[\w.+-]*)$/.exec(spec)?.[1] || null;
  if (eco === "cargo") return /^=\s*(\d[\w.+-]*)$/.exec(spec)?.[1] || null;
  if (eco === "maven") return /^[\w.+-]+$/.test(spec) && !spec.includes("$") ? spec : null;
  return null;
}
