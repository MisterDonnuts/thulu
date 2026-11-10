// Keys must be literals at the call site: scripts/i18n-guard.test.mjs checks each one exists and
// every catalog key is used.

import { locale, locales, messages, sourceLocale, type Message } from "virtual:octopus-messages";

export { locale, locales, sourceLocale };

export type Params = Record<string, string | number | boolean | null | undefined>;

const plurals = new Intl.PluralRules(locale);
const placeholder = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

function pick(msg: Message, params: Params | undefined): string {
  if (typeof msg === "string") return msg;
  const count = Number(params?.count ?? NaN);
  return msg[plurals.select(count)] ?? msg.other ?? "";
}

export function t(key: string, params?: Params): string {
  if (!Object.hasOwn(messages, key)) return key;
  const msg = messages[key];
  const tmpl = pick(msg, params);
  if (!params) return tmpl;
  return tmpl.replace(placeholder, (whole, name: string) => {
    const v = params[name];
    return v === undefined || v === null ? whole : String(v);
  });
}

export const SLOT = "\uE000";

const slotMark = /\uE000(\w+)\uE000/;

export function tParts(key: string, params: Params): string[] {
  const marked: Params = {};
  for (const [name, v] of Object.entries(params))
    marked[name] = v === SLOT ? `${SLOT}${name}${SLOT}` : v;
  return t(key, marked).split(slotMark);
}

export function has(key: string): boolean {
  return Object.hasOwn(messages, key);
}

// Must match the cookie name the daemon reads to pick the dashboard's language.
export const LOCALE_COOKIE = "octopus_locale";

export function switchLocale(next: string): void {
  document.cookie = `${LOCALE_COOKIE}=${encodeURIComponent(next)}; path=/; max-age=31536000; samesite=lax`;
  location.reload();
}
