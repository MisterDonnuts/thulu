// Every server under tools/ carries an identical copy of this file (each Docker build sees only its
// own dir);
// web/scripts/i18n-tools-guard.test.mjs fails when the copies drift.

import { AsyncLocalStorage } from "node:async_hooks";
import { readdirSync, readFileSync } from "node:fs";

export const SOURCE_LOCALE = "en";

const LOCALES_DIR = new URL("./locales/", import.meta.url);
const placeholder = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;
const pluralCategories = new Set([
  "zero",
  "one",
  "two",
  "few",
  "many",
  "other",
]);

function isPlural(node) {
  const keys = Object.keys(node);
  return (
    keys.includes("other") &&
    keys.every((k) => pluralCategories.has(k) && typeof node[k] === "string")
  );
}

export function flatten(node, prefix = "", out = {}) {
  for (const [k, v] of Object.entries(node)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string" || isPlural(v)) out[key] = v;
    else if (v && typeof v === "object") flatten(v, key, out);
    else throw new Error(`i18n: ${key} is neither a message nor a group`); // i18n-ignore the catalog's own loader
  }
  return out;
}

export function loadCatalogs(dir = LOCALES_DIR) {
  const catalogs = {};
  for (const name of readdirSync(dir)) {
    if (!name.endsWith(".json")) continue;
    catalogs[name.slice(0, -5)] = flatten(
      JSON.parse(readFileSync(new URL(name, dir), "utf8")),
    );
  }
  return catalogs;
}

const catalogs = loadCatalogs();

function pickLocale(wanted) {
  const base = (wanted || "").toLowerCase().split(/[-_.]/)[0];
  return Object.hasOwn(catalogs, base) ? base : SOURCE_LOCALE;
}

export const locale = pickLocale(process.env.OCTOPUS_LOCALE);

const requestLocale = new AsyncLocalStorage();

export function withLocale(wanted, fn) {
  return wanted ? requestLocale.run(pickLocale(wanted), fn) : fn();
}

function render(loc, key, params) {
  const msg = catalogs[loc]?.[key] ?? catalogs[SOURCE_LOCALE]?.[key];
  if (msg === undefined) return key;
  let tmpl = msg;
  if (typeof msg !== "string") {
    const count = Number(params?.count ?? NaN);
    tmpl = msg[new Intl.PluralRules(loc).select(count)] ?? msg.other;
  }
  if (!params) return tmpl;
  return tmpl.replace(placeholder, (whole, name) => {
    const v = params[name];
    return v === undefined || v === null ? whole : String(v);
  });
}

export function t(key, params) {
  return render(requestLocale.getStore() ?? locale, key, params);
}

export function tLog(key, params) {
  return render(SOURCE_LOCALE, key, params);
}
