import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = join(dirname(fileURLToPath(import.meta.url)), "..", "src");
const MODIFIER = /[⌘⌥]|\bCtrl\b|\bCmd\b/;

function walk(dir, keep) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return walk(p, keep);
    return keep(name) ? [p] : [];
  });
}

const commentLine = /^\s*(\/\/|\*|\/\*|<!--)/;

describe("platform keys", () => {
  it("are written in lib/shortcuts.ts only", () => {
    const found = [];
    const files = walk(src, (n) => /\.(ts|astro)$/.test(n) && !/\.test\.ts$/.test(n));
    for (const file of files) {
      const rel = relative(src, file);
      if (rel === join("lib", "shortcuts.ts")) continue;
      readFileSync(file, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (!commentLine.test(line) && MODIFIER.test(line)) found.push(`${rel}:${i + 1}`);
        });
    }
    expect(found).toEqual([]);
  });

  it("stay out of the catalog, but for the word Ctrl itself", () => {
    const found = [];
    const visit = (obj, path, file) => {
      for (const [k, v] of Object.entries(obj)) {
        const key = path ? `${path}.${k}` : k;
        if (v && typeof v === "object") visit(v, key, file);
        else if (typeof v === "string" && MODIFIER.test(v) && key !== "kbd.ctrl")
          found.push(`${file}: ${key}`);
      }
    };
    for (const file of walk(join(src, "i18n", "locales"), (n) => n.endsWith(".json")))
      visit(JSON.parse(readFileSync(file, "utf8")), "", relative(src, file));
    expect(found).toEqual([]);
  });
});
