// Each server's Docker build sees only its own directory, so i18n.js is copied from octopus-api,
// not shared.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { createScanner, localeReport } from "./i18n-scan.mjs";

const TOOLS = fileURLToPath(new URL("../../tools", import.meta.url));
const SOURCE_LOCALE = "en";
const CANONICAL = join(TOOLS, "octopus-api", "i18n.js");
const SKIP_DIRS = new Set(["node_modules", "test", "locales"]);
const pluralCategories = new Set(["zero", "one", "two", "few", "many", "other"]);

function flatten(node, prefix = "", out = {}) {
  for (const [k, v] of Object.entries(node)) {
    const key = prefix ? `${prefix}.${k}` : k;
    const plural =
      v &&
      typeof v === "object" &&
      "other" in v &&
      Object.entries(v).every(([c, f]) => pluralCategories.has(c) && typeof f === "string");
    if (typeof v === "string" || plural) out[key] = v;
    else flatten(v, key, out);
  }
  return out;
}

function servers() {
  return readdirSync(TOOLS).filter((d) => existsSync(join(TOOLS, d, "server.js")));
}

function codeFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (!SKIP_DIRS.has(name)) out.push(...codeFiles(p));
    } else if (/\.m?js$/.test(name) && !/\.test\.m?js$/.test(name) && name !== "i18n.js")
      out.push(p);
  }
  return out;
}

function catalogs(server) {
  const dir = join(TOOLS, server, "locales");
  if (!existsSync(dir)) return {};
  const out = {};
  for (const name of readdirSync(dir))
    if (name.endsWith(".json"))
      out[name.slice(0, -5)] = flatten(JSON.parse(readFileSync(join(dir, name), "utf8")));
  return out;
}

function check(server) {
  const root = join(TOOLS, server);
  const report = [];
  const helper = join(root, "i18n.js");
  if (!existsSync(helper)) report.push(`${server}: no i18n.js`);
  else if (readFileSync(helper, "utf8") !== readFileSync(CANONICAL, "utf8"))
    report.push(`${server}/i18n.js differs from octopus-api/i18n.js`);
  const dockerfile = join(root, "Dockerfile");
  if (existsSync(dockerfile)) {
    const d = readFileSync(dockerfile, "utf8");
    if (!/COPY[^\n]*i18n\.js/.test(d) || !/COPY[^\n]*locales/.test(d))
      report.push(`${server}/Dockerfile does not copy i18n.js and locales/`);
  }
  const all = catalogs(server);
  const source = all[SOURCE_LOCALE];
  if (!source) return [...report, `${server}: no locales/${SOURCE_LOCALE}.json`];
  const scan = createScanner(source);
  for (const path of codeFiles(root))
    scan.checkScript(relative(TOOLS, path), readFileSync(path, "utf8"), 0);
  report.push(...scan.problems.map((p) => `tools/${p.file}:${p.line}: ${p.msg}`));
  const unused = Object.keys(source).filter((k) => !scan.used.has(k));
  if (unused.length) report.push(`${server} (catalog): keys no code names: ${unused.join(", ")}`);
  for (const [loc, msgs] of Object.entries(all))
    if (loc !== SOURCE_LOCALE) report.push(...localeReport(`${server} ${loc}`, source, msgs));
  return report;
}

const scope = process.env.I18N_GUARD_PATH ?? "";

describe("MCP servers' i18n guard", () => {
  it("finds no text outside each server's catalog", () => {
    const report = servers()
      .filter((s) => s.startsWith(scope))
      .flatMap(check);
    expect(report, `${report.length} i18n problems`).toEqual([]);
  }, 120_000);
});
