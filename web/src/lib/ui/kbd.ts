// Keep in sync with Kbd.astro and styles/primitives/kbd.css.

import { ariaKeyShortcuts, hintKeys, keyLabel, modKeyAttr } from "../shortcuts";
import { attrsHTML, esc } from "./dom";

export type KbdSize = "sm" | "md";

export type KbdAlign = "start" | "end";

const ALIGNS = new Set<string>(["start", "end"]);

export interface KbdLook {
  size?: KbdSize;
  inset?: boolean;
  cls?: string;
}

export interface KbdOptions extends KbdLook {
  align?: KbdAlign;
  decorative?: boolean;
}

export function kbdClasses(size: KbdSize = "md", cls?: string, inset = false): string | undefined {
  return (
    [size === "sm" && "kbd-sm", inset && "kbd-inset", cls].filter(Boolean).join(" ") || undefined
  );
}

export function kbdGroupClasses(
  o: { align?: KbdAlign; inset?: boolean; cls?: string } = {},
): string {
  const align = o.align ?? "start";
  if (!ALIGNS.has(align)) throw new Error(`kbd: no align "${align}"`); // i18n-ignore a programming error
  if (o.inset) throw new Error("kbd: only one key sits inside a field"); // i18n-ignore a programming error
  return ["kbd-group", align === "end" && "kbd-group-end", o.cls].filter(Boolean).join(" ");
}

export function kbdKeys(keys: string | string[]): string | string[] {
  if (typeof keys !== "string") return keys;
  const split = hintKeys(keys);
  return split.length > 1 ? split : keys;
}

// data-mod-key lets lib/shortcuts.ts relabel ⌘/⌥ on pages rendered at build time.
export function kbdHTML(keys: string | string[], opts: KbdOptions = {}): string {
  const hidden = opts.decorative ? "true" : undefined;
  const k = kbdKeys(keys);
  if (typeof k === "string") {
    return capHTML(k, kbdClasses(opts.size, opts.cls, opts.inset), hidden);
  }
  const cls = kbdGroupClasses(opts);
  const caps = k.map((key) => capHTML(key, kbdClasses(opts.size))).join("");
  return `<span${attrsHTML({ class: cls, "aria-hidden": hidden })}>${caps}</span>`;
}

function capHTML(key: string, cls: string | undefined, hidden?: string): string {
  const attrs = { class: cls, "data-mod-key": modKeyAttr(key), "aria-hidden": hidden };
  return `<kbd${attrsHTML(attrs)}>${esc(keyLabel(key))}</kbd>`;
}

// aria-keyshortcuts reads spaces as alternatives, so a sequence ("G T") gets none.
export function keyShortcuts(hint: string | string[] | undefined): string | undefined {
  return ariaKeyShortcuts(typeof hint === "string" ? hint.trim() : hint);
}
