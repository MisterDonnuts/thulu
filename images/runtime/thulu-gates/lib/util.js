// Small shared helpers: globs, file classes, line ranges, fingerprints.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { extname, join } from "node:path";

// globToRegExp supports **, *, ?, {a,b} and a leading or embedded "/". A pattern without "/"
// matches the basename anywhere in the tree, the way .gitignore reads it.
export function globToRegExp(glob) {
  let g = glob.replace(/^\.\//, "");
  const anchored = g.includes("/");
  let re = "";
  for (let i = 0; i < g.length; i++) {
    const c = g[i];
    if (c === "*") {
      if (g[i + 1] === "*") {
        const slash = g[i + 2] === "/";
        re += slash ? "(?:.*/)?" : ".*";
        i += slash ? 2 : 1;
      } else re += "[^/]*";
    } else if (c === "?") re += "[^/]";
    else if (c === "{") {
      const end = g.indexOf("}", i);
      if (end < 0) {
        re += "\\{";
        continue;
      }
      re += `(?:${g
        .slice(i + 1, end)
        .split(",")
        .map((s) => s.replace(/[.+^$()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*"))
        .join("|")})`;
      i = end;
    } else re += c.replace(/[.+^$()|[\]\\]/g, "\\$&");
  }
  return new RegExp(anchored ? `^${re}$` : `(?:^|/)${re}$`);
}

export function matchesAny(path, globs) {
  return globs.some((g) => globToRegExp(g).test(path));
}

// The test-file globs of the locked-tests contract (directory globs match at any depth).
export const TEST_GLOBS = [
  "*_test.go",
  "*.test.{ts,tsx,js,jsx,mjs,cjs}",
  "*.spec.*",
  "test_*.py",
  "*_test.py",
  "**/tests/**",
  "**/__tests__/**",
  "**/src/test/**",
  "*Test.java",
];

export function isTestFile(path) {
  return matchesAny(path, TEST_GLOBS) || /(^|\/)(testdata|fixtures)\//.test(path);
}

const LANG_BY_EXT = {
  ".go": "go",
  ".js": "js",
  ".jsx": "js",
  ".mjs": "js",
  ".cjs": "js",
  ".ts": "ts",
  ".tsx": "ts",
  ".mts": "ts",
  ".cts": "ts",
  ".py": "python",
  ".rs": "rust",
  ".java": "java",
  ".kt": "kotlin",
  ".kts": "kotlin",
  ".rb": "ruby",
  ".php": "php",
  ".cs": "csharp",
  ".swift": "swift",
  ".c": "c",
  ".h": "c",
  ".cc": "cpp",
  ".cpp": "cpp",
  ".hpp": "cpp",
  ".sh": "shell",
};

export function langOf(path) {
  return LANG_BY_EXT[extname(path).toLowerCase()] || null;
}

export function isSourceFile(path) {
  return langOf(path) !== null;
}

// ranges collapses a set of line numbers into [start, end] pairs.
export function ranges(lines) {
  const sorted = [...lines].sort((a, b) => a - b);
  const out = [];
  for (const n of sorted) {
    const last = out[out.length - 1];
    if (last && n === last[1] + 1) last[1] = n;
    else out.push([n, n]);
  }
  return out;
}

export function formatRanges(rs, max = 12) {
  const parts = rs.slice(0, max).map(([a, b]) => (a === b ? `${a}` : `${a}-${b}`));
  if (rs.length > max) parts.push(`+${rs.length - max}`);
  return parts.join(", ");
}

const fileCache = new Map();

// lineText returns line n (1-based) of root/path, or "" when unreadable.
export function lineText(root, path, n) {
  const key = join(root, path);
  let lines = fileCache.get(key);
  if (!lines) {
    try {
      lines = readFileSync(key, "utf8").split("\n");
    } catch {
      lines = [];
    }
    fileCache.set(key, lines);
  }
  return lines[n - 1] ?? "";
}

export function readText(path) {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return null;
  }
}

export function sha256(s) {
  return createHash("sha256").update(s).digest("hex");
}

// fingerprint is stable across line shifts: it hashes the tool, rule, file and the trimmed text
// of the flagged line, not the line number.
export function fingerprint(tool, rule, file, text) {
  return sha256(`${tool}\0${rule}\0${file}\0${(text || "").trim()}`).slice(0, 32);
}

// firstLine is the line a summary quotes from a tool's output: the first non-empty one, except
// when the output has a Python traceback: then it is the exception line, the first one after the
// last traceback header that is not indented like the frames (mutmut prints a progress spinner
// before the traceback and a second message line after the exception).
export function firstLine(s, max = 300) {
  const lines = (s || "").trim().split("\n").filter((x) => x.trim());
  let l = lines[0] || "";
  const tb = lines.findLastIndex((x) => x.startsWith("Traceback (most recent call last)"));
  if (tb >= 0) l = (lines.slice(tb + 1).find((x) => !/^\s/.test(x)) || lines[lines.length - 1]).trim();
  return l.length > max ? `${l.slice(0, max - 1)}…` : l;
}

// errorLine is the line that says why a build tool failed: the first "error..." line (cargo,
// rustc) or "[ERROR] ..." line (Maven) that is more than the BUILD FAILURE banner. cargo and
// cargo-llvm-cov print progress and notes first ("Updating crates.io index", "info: ... setting
// cfg(coverage)"), so the first line of stderr is rarely it.
export function errorLine(text, max = 300) {
  const lines = (text || "")
    .split("\n")
    .map((l) => l.replace(/\x1b\[[0-9;]*m/g, "").trim())
    .filter(Boolean);
  const hit = lines.find((l) => /^error(?:\[\w+\])?:/i.test(l) || (/^\[ERROR\]/.test(l) && !/^\[ERROR\]\s*(?:BUILD FAILURE)?$/.test(l)));
  return hit ? firstLine(hit, max) : firstLine(text, max);
}

export function tryJSON(s) {
  try {
    return JSON.parse(s);
  } catch {
    return undefined;
  }
}

// jsonLines parses a stream of JSON objects separated by newlines (or concatenated, as
// govulncheck -json prints them).
export function jsonStream(s) {
  const out = [];
  let depth = 0;
  let start = -1;
  let inStr = false;
  let esc = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inStr) {
      if (esc) esc = false;
      else if (c === "\\") esc = true;
      else if (c === '"') inStr = false;
      continue;
    }
    if (c === '"') inStr = true;
    else if (c === "{") {
      if (depth === 0) start = i;
      depth++;
    } else if (c === "}") {
      depth--;
      if (depth === 0 && start >= 0) {
        const v = tryJSON(s.slice(start, i + 1));
        if (v !== undefined) out.push(v);
        start = -1;
      }
    }
  }
  return out;
}

export function relTo(root, p) {
  if (!p) return p;
  let s = String(p).replace(/^file:\/\//, "");
  if (s.startsWith(root + "/")) s = s.slice(root.length + 1);
  return s.replace(/^\.\//, "");
}
