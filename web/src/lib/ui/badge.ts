// badgeHTML() and dotHTML() must write the same markup as Badge.astro and Dot.astro.

import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type BadgeTone = "neutral" | "ok" | "warn" | "danger" | "muted" | "accent" | "info";

export type BadgeShape = "rect" | "pill";

export type BadgeSize = "sm" | "md";

export type BadgeEdge = "solid" | "dashed" | "hollow";

export type DotTone =
  | "neutral"
  | "ok"
  | "warn"
  | "danger"
  | "accent"
  | "info"
  | "special"
  | "empty";

const TONES = new Set<string>(["neutral", "ok", "warn", "danger", "muted", "accent", "info"]);
const SHAPES = new Set<string>(["rect", "pill"]);
const SIZES = new Set<string>(["sm", "md"]);
const EDGES = new Set<string>(["solid", "dashed", "hollow"]);
const DOT_TONES = new Set<string>([
  "neutral",
  "ok",
  "warn",
  "danger",
  "accent",
  "info",
  "special",
  "empty",
]);

export interface BadgeLook {
  tone?: BadgeTone;
  shape?: BadgeShape;
  size?: BadgeSize;
  mono?: boolean;
  edge?: BadgeEdge;
  raised?: boolean;
  cls?: string;
}

function check(set: Set<string>, value: string, what: string): void {
  if (!set.has(value)) throw new Error(`badge: no ${what} "${value}"`); // i18n-ignore a programming error
}

export function badgeClasses(look: BadgeLook = {}): string {
  const {
    tone = "neutral",
    shape = "rect",
    size = "md",
    mono = false,
    edge,
    raised = false,
    cls,
  } = look;
  check(TONES, tone, "tone");
  check(SHAPES, shape, "shape");
  check(SIZES, size, "size");
  if (edge) check(EDGES, edge, "edge");
  return [
    "badge",
    tone !== "neutral" && `badge-${tone}`,
    shape === "pill" && "badge-pill",
    size === "sm" && "badge-sm",
    mono && "badge-mono",
    edge === "solid" ? "badge-edge" : edge && `badge-${edge}`,
    raised && "badge-raised",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function dotClasses(tone: DotTone = "neutral", pulse = false, cls?: string): string {
  if (!DOT_TONES.has(tone)) throw new Error(`dot: no tone "${tone}"`); // i18n-ignore a programming error
  return ["dot", tone !== "neutral" && `dot-${tone}`, pulse && "dot-pulse", cls]
    .filter(Boolean)
    .join(" ");
}

export interface DotOptions {
  pulse?: boolean;
  label?: string;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function dotA11y(label?: string): Attrs {
  return label ? { role: "img", "aria-label": label, title: label } : { "aria-hidden": "true" };
}

export function dotHTML(tone: DotTone = "neutral", opts: DotOptions = {}): string {
  const own: Attrs = {
    id: opts.id,
    class: dotClasses(tone, opts.pulse, opts.cls),
    ...dotA11y(opts.label),
  };
  return `<span${attrsHTML(own)}${attrsHTML(opts.attrs)}></span>`;
}

export function statusGlyph(icon?: string, tone: DotTone = "neutral"): string {
  return icon ? icoSvg(icon) : dotHTML(tone, { pulse: true });
}

export interface BadgeOptions extends Omit<BadgeLook, "tone"> {
  icon?: string;
  spin?: boolean;
  dot?: boolean | DotTone;
  pulse?: boolean;
  title?: string;
  id?: string;
  attrs?: Attrs;
}

export function badgeGlyph(o: Pick<BadgeOptions, "icon" | "spin" | "dot" | "pulse">): string {
  if (o.icon) return icoSvg(o.icon, o.spin ? { cls: "badge-spin" } : {});
  if (!o.dot && !o.pulse) return "";
  const tone = typeof o.dot === "string" ? o.dot : "neutral";
  return dotHTML(tone, { pulse: o.pulse });
}

export function badgeHTML(
  label: string,
  tone: BadgeTone = "neutral",
  opts: BadgeOptions = {},
): string {
  const own: Attrs = {
    id: opts.id,
    class: badgeClasses({ ...opts, tone }),
    title: opts.title,
  };
  return `<span${attrsHTML(own)}${attrsHTML(opts.attrs)}>${badgeGlyph(opts)}${esc(label)}</span>`;
}

// Rewrites the whole class list: placement classes must be passed in opts.cls.
export function setBadge(
  el: HTMLElement,
  label: string,
  tone: BadgeTone = "neutral",
  opts: Omit<BadgeOptions, "id" | "attrs"> = {},
): void {
  el.className = badgeClasses({ ...opts, tone });
  el.innerHTML = badgeGlyph(opts) + esc(label);
  if (opts.title) el.title = opts.title;
  else el.removeAttribute("title");
}
