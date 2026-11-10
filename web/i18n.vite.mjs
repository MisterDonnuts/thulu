import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const LOCALES_DIR = fileURLToPath(new URL("./src/i18n/locales", import.meta.url));
export const SOURCE_LOCALE = "en";
const VIRTUAL_ID = "virtual:octopus-messages";
const RESOLVED_ID = "\0" + VIRTUAL_ID;

function jsonFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...jsonFiles(p));
    else if (name.endsWith(".json")) out.push(p);
  }
  return out;
}

const PLURAL_CATEGORIES = new Set(["zero", "one", "two", "few", "many", "other"]);

function isPlural(node) {
  const keys = Object.keys(node);
  return (
    keys.includes("other") &&
    keys.every((k) => PLURAL_CATEGORIES.has(k) && typeof node[k] === "string")
  );
}

function flatten(prefix, node, out, file) {
  for (const [k, v] of Object.entries(node)) {
    const key = `${prefix}.${k}`;
    if (key in out) throw new Error(`${file}: ${key} is defined twice`);
    if (typeof v === "string") out[key] = v;
    else if (v && typeof v === "object" && isPlural(v)) out[key] = v;
    else if (v && typeof v === "object") flatten(key, v, out, file);
    else throw new Error(`${file}: ${key}: a message is a string or an object`);
  }
}

export function availableLocales() {
  return readdirSync(LOCALES_DIR).filter((n) => statSync(join(LOCALES_DIR, n)).isDirectory()).sort();
}

export function loadMessages(locale) {
  const dir = join(LOCALES_DIR, locale);
  const out = {};
  for (const file of jsonFiles(dir)) {
    const ns = relative(dir, file).slice(0, -".json".length).split(sep).join(".");
    flatten(ns, JSON.parse(readFileSync(file, "utf8")), out, file);
  }
  return out;
}

export function buildLocale() {
  const l = process.env.OCTOPUS_LOCALE || SOURCE_LOCALE;
  if (!availableLocales().includes(l)) {
    throw new Error(`OCTOPUS_LOCALE=${l}: no catalog in ${LOCALES_DIR}`);
  }
  return l;
}

export function octopusMessages() {
  return {
    name: "octopus:messages",
    resolveId(id) {
      return id === VIRTUAL_ID ? RESOLVED_ID : undefined;
    },
    load(id) {
      if (id !== RESOLVED_ID) return undefined;
      const locale = buildLocale();
      for (const f of jsonFiles(LOCALES_DIR)) this.addWatchFile(f);
      return [
        `export const locale = ${JSON.stringify(locale)};`,
        `export const sourceLocale = ${JSON.stringify(SOURCE_LOCALE)};`,
        `export const locales = ${JSON.stringify(availableLocales())};`,
        `export const messages = ${JSON.stringify(loadMessages(locale))};`,
      ].join("\n");
    },
  };
}
