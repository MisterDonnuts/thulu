// The repo's i18n guards scan Go and the MCP servers under tools/; this tool lives elsewhere, so
// it checks its own catalogs: same keys and placeholders in every language, and every key the
// code asks for exists.

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { loadCatalogs, setLocale, SOURCE_LOCALE, t } from "../lib/i18n.js";

const LIB = fileURLToPath(new URL("../lib/", import.meta.url));
const catalogs = loadCatalogs();

function sources(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? sources(join(dir, e.name)) : e.name.endsWith(".js") ? [join(dir, e.name)] : []));
}

function placeholders(msg) {
  const texts = typeof msg === "string" ? [msg] : Object.values(msg);
  return [...new Set(texts.flatMap((s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1])))].sort();
}

test("every locale has the source's keys, plural shape and placeholders", () => {
  const en = catalogs[SOURCE_LOCALE];
  assert.ok(Object.keys(catalogs).includes("es"));
  for (const [name, cat] of Object.entries(catalogs)) {
    assert.deepEqual(Object.keys(cat).sort(), Object.keys(en).sort(), `${name}: keys differ from ${SOURCE_LOCALE}`);
    for (const k of Object.keys(en)) {
      assert.equal(typeof cat[k], typeof en[k], `${name}: ${k} plural shape differs`);
      assert.deepEqual(placeholders(cat[k]), placeholders(en[k]), `${name}: ${k} placeholders differ`);
    }
  }
});

test("every key the code passes to t() exists in the source catalog", () => {
  const en = catalogs[SOURCE_LOCALE];
  const keys = Object.keys(en);
  const missing = [];
  let seen = 0;
  for (const file of sources(LIB)) {
    const text = readFileSync(file, "utf8");
    for (const m of text.matchAll(/\bt\(\s*(["`])([a-z_]+\.[a-z_.]*?)(\$\{[^}]+\})?\1/g)) {
      const [, , key, dynamic] = m;
      seen++;
      const ok = dynamic ? keys.some((k) => k.startsWith(key)) : Object.hasOwn(en, key);
      if (!ok) missing.push(`${file.slice(LIB.length)}: ${key}${dynamic || ""}`);
    }
  }
  assert.ok(seen > 100, `the scan found only ${seen} t() calls`);
  assert.deepEqual(missing, []);
});

test("t() interpolates, picks plurals per locale and falls back to English", () => {
  setLocale("es");
  try {
    assert.equal(t("tests.failed", { count: 1 }), "1 test falla");
    assert.equal(t("tests.failed", { count: 3 }), "3 tests fallan");
    assert.equal(t("no.such.key"), "no.such.key");
  } finally {
    setLocale("en");
  }
  assert.equal(t("common.missing_tool", { tools: "semgrep", install: "x" }), "not installed: semgrep. Install: x");
});
