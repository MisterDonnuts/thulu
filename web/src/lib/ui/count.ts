// Keep in sync with Count.astro and styles/primitives/count.css.

import { locale } from "../../i18n";
import { badgeGlyph, type DotTone } from "./badge";
import { attrsHTML, esc, type Attrs } from "./dom";

export type CountTone = "neutral" | "accent" | "warn" | "danger";

export type CountSize = "sm" | "md";

const TONES = new Set<string>(["neutral", "accent", "warn", "danger"]);
const SIZES = new Set<string>(["sm", "md"]);

export interface CountLook {
  pill?: boolean;
  tone?: CountTone;
  size?: CountSize;
  cls?: string;
}

export function countClasses(look: CountLook = {}): string {
  const { pill = false, tone = "neutral", size = "md", cls } = look;
  if (!TONES.has(tone)) throw new Error(`count: no tone "${tone}"`); // i18n-ignore a programming error
  if (!SIZES.has(size)) throw new Error(`count: no size "${size}"`); // i18n-ignore a programming error
  if (!pill && (tone !== "neutral" || size !== "md"))
    throw new Error("count: a tone or a size needs pill"); // i18n-ignore a programming error
  const parts = pill
    ? ["count-pill", size === "sm" && "count-sm", tone !== "neutral" && `count-${tone}`]
    : ["count"];
  return [...parts, cls].filter(Boolean).join(" ");
}

export function countText(n: number | string, max?: number): string {
  if (typeof n === "string") return n;
  if (max !== undefined && n > max) return `${max.toLocaleString(locale)}+`;
  return n.toLocaleString(locale);
}

export function countHidden(n: number | string, hideZero = false): boolean {
  return hideZero && (n === 0 || n === "0");
}

export interface CountOptions extends CountLook {
  label?: string;
  srLabel?: string;
  dot?: boolean | DotTone;
  pulse?: boolean;
  max?: number;
  hideZero?: boolean;
  title?: string;
  id?: string;
  attrs?: Attrs;
}

export function countInnerHTML(n: number | string, o: CountOptions = {}): string {
  if (!o.pill && (o.dot || o.pulse || o.label))
    throw new Error("count: a dot or a label needs pill"); // i18n-ignore a programming error
  const num = esc(countText(n, o.max));
  const glyph = o.pill ? badgeGlyph({ dot: o.dot, pulse: o.pulse }) : "";
  const body = o.label ? `<strong>${num}</strong> ${esc(o.label)}` : num;
  const sr = o.srLabel ? `<span class="visually-hidden"> ${esc(o.srLabel)}</span>` : "";
  return glyph + body + sr;
}

export function countHTML(n: number | string, o: CountOptions = {}): string {
  const own: Attrs = {
    id: o.id,
    class: countClasses(o),
    title: o.title,
    hidden: countHidden(n, o.hideZero),
  };
  return `<span${attrsHTML(own)}${attrsHTML(o.attrs)}>${countInnerHTML(n, o)}</span>`;
}

export function setCount(
  el: HTMLElement,
  n: number | string,
  o: Omit<CountOptions, "id" | "attrs"> = {},
): void {
  el.className = countClasses(o);
  el.innerHTML = countInnerHTML(n, o);
  el.hidden = countHidden(n, o.hideZero);
  if (o.title) el.title = o.title;
  else el.removeAttribute("title");
}
