import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { availableLocales, loadMessages, SOURCE_LOCALE } from "../i18n.vite.mjs";
import { createScanner, localeReport } from "./i18n-scan.mjs";

const SRC = fileURLToPath(new URL("../src", import.meta.url));
const source = loadMessages(SOURCE_LOCALE);

function sourceFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name !== "i18n") out.push(...sourceFiles(p));
    } else if (/\.(ts|astro)$/.test(name) && !/\.(test|d)\.ts$/.test(name)) out.push(p);
  }
  return out;
}

const scope = process.env.I18N_GUARD_PATH ?? "";

describe("i18n guard", () => {
  it("finds no text outside the catalog, and no catalog key outside the code", async () => {
    const scan = createScanner(source);
    for (const path of sourceFiles(SRC)) {
      const file = relative(SRC, path);
      const code = readFileSync(path, "utf8");
      if (path.endsWith(".astro")) await scan.checkAstro(file, code);
      else scan.checkScript(file, code, 0);
    }
    const report = scan.problems
      .filter((p) => p.file.startsWith(scope))
      .map((p) => `src/${p.file}:${p.line}: ${p.msg}`);
    if (!scope) {
      const unused = Object.keys(source).filter((k) => !scan.used.has(k));
      if (unused.length) report.push(`(catalog): keys no code names: ${unused.join(", ")}`);
    }
    expect(report, `${report.length} i18n problems`).toEqual([]);
  }, 120_000);

  it("gives every locale the source's keys with the same variables", () => {
    const report = availableLocales()
      .filter((loc) => loc !== SOURCE_LOCALE)
      .flatMap((loc) => localeReport(loc, source, loadMessages(loc)));
    expect(report).toEqual([]);
  });
});
