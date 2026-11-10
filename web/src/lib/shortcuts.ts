// No browser imports: KbdHelp.astro renders globalRows() at build time.
// lib/keyboard.ts acts on GO_TARGETS; keep globalRows() in sync with it.

import { t } from "../i18n";

export type KbdRow = { keys: string[]; label: string } | { sep: string };

export const MOD_KEY = "⌘";

export const ALT_KEY = "⌥";

const PLATFORM_KEYS: readonly string[] = [MOD_KEY, ALT_KEY];

export function isApplePlatform(platform: string): boolean {
  return /mac|iphone|ipad/i.test(platform);
}

export function browserPlatform(): string | null {
  if (typeof document === "undefined" || typeof navigator === "undefined") return null;
  const uaData = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData;
  return [uaData?.platform, navigator.platform, navigator.userAgent].find((p) => p) ?? "";
}

export function onApplePlatform(platform: string | null = browserPlatform()): boolean {
  return platform === null || isApplePlatform(platform);
}

export function isPlatformKey(key: string): boolean {
  return PLATFORM_KEYS.includes(key);
}

export function keyLabel(key: string, apple = onApplePlatform()): string {
  if (apple || !isPlatformKey(key)) return key;
  return key === MOD_KEY ? t("common.kbd.ctrl") : t("common.kbd.alt");
}

export function altKeyWord(apple = onApplePlatform()): string {
  return apple ? t("common.kbd.option") : t("common.kbd.alt");
}

export function hintKeys(hint: string): string[] {
  const h = hint.trim();
  const mod = PLATFORM_KEYS.find((k) => h.startsWith(k) && h.length > k.length);
  return mod ? [mod, h.slice(mod.length).trim()] : [h];
}

export function chordText(keys: string | string[], apple = onApplePlatform()): string {
  const list = typeof keys === "string" ? hintKeys(keys) : keys;
  return list.map((k) => keyLabel(k, apple)).join(apple ? "" : "+");
}

const ARIA_NAMES: Readonly<Record<string, string>> = {
  "←": "ArrowLeft",
  "→": "ArrowRight",
  "↑": "ArrowUp",
  "↓": "ArrowDown",
};

// A space separates alternatives in aria-keyshortcuts, so a sequence ("G T") gets undefined.
// ⌘ maps to both Meta and Control because the handlers accept both.
export function ariaKeyShortcuts(keys: string | string[] | undefined): string | undefined {
  const list = typeof keys === "string" ? hintKeys(keys) : (keys ?? []);
  if (!list.length || list.some((k) => !k || /\s/.test(k))) return undefined;
  const rest = list
    .filter((k) => !isPlatformKey(k))
    .map((k) => ARIA_NAMES[k] ?? k)
    .join("+");
  if (list.includes(MOD_KEY)) return `Meta+${rest} Control+${rest}`;
  if (list.includes(ALT_KEY)) return `Alt+${rest}`;
  return rest;
}

export function applyPlatformKeys(root: ParentNode = document, apple = onApplePlatform()): void {
  root.querySelectorAll<HTMLElement>("kbd[data-mod-key]").forEach((el) => {
    el.textContent = keyLabel(el.dataset.modKey === "alt" ? ALT_KEY : MOD_KEY, apple);
  });
}

export function modKeyAttr(key: string): "mod" | "alt" | undefined {
  return key === MOD_KEY ? "mod" : key === ALT_KEY ? "alt" : undefined;
}

export const GO_TARGETS: Readonly<Record<string, string>> = {
  d: "/",
  t: "/tasks/",
  p: "/projects/",
  i: "/inbox/",
  e: "/evidence/",
  a: "/agents/",
  s: "/settings/",
};

export function globalRows(): KbdRow[] {
  return [
    { sep: t("common.kbd.navigation") },
    { keys: [MOD_KEY, "K"], label: t("common.kbd.palette") },
    { keys: ["G", "D"], label: t("common.nav.dashboard") },
    { keys: ["G", "T"], label: t("common.nav.tasks") },
    { keys: ["G", "P"], label: t("common.nav.projects") },
    { keys: ["G", "I"], label: t("common.nav.inbox") },
    { keys: ["G", "E"], label: t("common.nav.evidence") },
    { keys: ["G", "A"], label: t("common.nav.agents") },
    { keys: ["G", "S"], label: t("common.nav.settings") },
    { keys: ["?"], label: t("common.kbd.show_help") },
  ];
}
