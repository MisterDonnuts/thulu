// Message catalog for the text a person or an agent reads: report.md, check summaries, doctor
// and CLI errors. OCTOPUS_LOCALE picks the language, as in the daemon and the MCP servers;
// English is the source and the fallback.

import { readdirSync, readFileSync } from "node:fs";

export const SOURCE_LOCALE = "en";
const LOCALES_DIR = new URL("../locales/", import.meta.url);
const placeholder = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;
const pluralCategories = new Set(["zero", "one", "two", "few", "many", "other"]);

function isPlural(node) {
  const keys = Object.keys(node);
  return keys.includes("other") && keys.every((k) => pluralCategories.has(k) && typeof node[k] === "string");
}

export function flatten(node, prefix = "", out = {}) {
  for (const [k, v] of Object.entries(node)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string" || isPlural(v)) out[key] = v;
    else if (v && typeof v === "object") flatten(v, key, out);
  }
  return out;
}

export function loadCatalogs() {
  const out = {};
  for (const name of readdirSync(LOCALES_DIR)) {
    if (!name.endsWith(".json")) continue;
    out[name.slice(0, -5)] = flatten(JSON.parse(readFileSync(new URL(name, LOCALES_DIR), "utf8")));
  }
  return out;
}

const catalogs = loadCatalogs();
let current = pick(process.env.OCTOPUS_LOCALE);

function pick(wanted) {
  const base = (wanted || "").toLowerCase().split(/[-_.]/)[0];
  return Object.hasOwn(catalogs, base) ? base : SOURCE_LOCALE;
}

export function setLocale(wanted) {
  current = pick(wanted);
}

export function locale() {
  return current;
}

export function t(key, params) {
  const msg = catalogs[current]?.[key] ?? catalogs[SOURCE_LOCALE]?.[key];
  if (msg === undefined) return key;
  let tmpl = msg;
  if (typeof msg !== "string") {
    const count = Number(params?.count ?? NaN);
    tmpl = msg[new Intl.PluralRules(current).select(count)] ?? msg.other;
  }
  if (!params) return tmpl;
  return tmpl.replace(placeholder, (whole, name) => {
    const v = params[name];
    return v === undefined || v === null ? whole : String(v);
  });
}
