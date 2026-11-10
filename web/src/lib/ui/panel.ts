// panelHTML() and sectionHeadHTML() must emit the same markup as Panel.astro and SectionHead.astro.

import { attrsHTML, esc, type Attrs } from "./dom";
import { countHTML, type CountOptions } from "./count";
import { icoSvg } from "./icon";

export type PanelSize = "md" | "sm";
export type PanelTone = "neutral" | "danger";
export type PanelPad = "sm" | "md";
export type PanelHeading = "h2" | "h3" | "h4" | "div";
export type PanelTag = "section" | "div" | "article";

const SIZES = new Set<string>(["md", "sm"]);
const TONES = new Set<string>(["neutral", "danger"]);
const PADS = new Set<string>(["sm", "md"]);
const HEADINGS = new Set<string>(["h2", "h3", "h4", "div"]);
const TAGS = new Set<string>(["section", "div", "article"]);

export interface PanelLook {
  size?: PanelSize;
  tone?: PanelTone;
  pick?: boolean;
  cls?: string;
}

export function panelClasses(look: PanelLook = {}): string {
  const { size = "md", tone = "neutral", pick: picked = false, cls } = look;
  if (!SIZES.has(size)) throw new Error(`panel: no size "${size}"`); // i18n-ignore a programming error
  if (!TONES.has(tone)) throw new Error(`panel: no tone "${tone}"`); // i18n-ignore a programming error
  if (picked && size !== "sm") throw new Error("panel: only a small panel is picked"); // i18n-ignore a programming error
  return [
    "panel",
    size === "sm" && "panel-sm",
    tone === "danger" && "panel-danger",
    picked && "panel-pick",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface PanelBodyLook {
  flush?: boolean;
  pad?: PanelPad;
  size?: PanelSize;
  cls?: string;
}

export function panelBodyClasses(look: PanelBodyLook = {}): string {
  const { flush = false, pad = "sm", size = "md", cls } = look;
  if (!PADS.has(pad)) throw new Error(`panel: no pad "${pad}"`); // i18n-ignore a programming error
  if (pad === "md" && flush) throw new Error("panel: a flush body has no pad"); // i18n-ignore a programming error
  if (pad === "md" && size === "sm") throw new Error("panel: a small panel has one padding"); // i18n-ignore a programming error
  return ["panel-body", flush && "panel-body-flush", pad === "md" && "panel-body-md", cls]
    .filter(Boolean)
    .join(" ");
}

export function panelHeading(as: PanelHeading = "h2"): PanelHeading {
  if (!HEADINGS.has(as)) throw new Error(`panel: no heading "${as}"`); // i18n-ignore a programming error
  return as;
}

export function panelTag(tag: PanelTag = "section"): PanelTag {
  if (!TAGS.has(tag)) throw new Error(`panel: no tag "${tag}"`); // i18n-ignore a programming error
  return tag;
}

export interface PanelOptions extends PanelLook {
  tag?: PanelTag;
  as?: PanelHeading;
  icon?: string;
  title?: string;
  titleHTML?: string;
  titleId?: string;
  statusHTML?: string;
  headHTML?: string;
  aside?: string;
  asideHTML?: string;
  actionsHTML?: string;
  body?: string;
  bodyHTML?: string;
  flush?: boolean;
  pad?: PanelPad;
  bodyCls?: string;
  bodyAttrs?: Attrs;
  foot?: string;
  footHTML?: string;
  id?: string;
  attrs?: Attrs;
}

const pick = (text: string | undefined, html: string | undefined): string =>
  text !== undefined ? esc(text) : (html ?? "");

export function panelHeadHTML(o: PanelOptions): string {
  const title = pick(o.title, o.titleHTML);
  const aside = pick(o.aside, o.asideHTML);
  if (![title, o.statusHTML, o.headHTML, aside, o.actionsHTML].some(Boolean)) return "";
  const h = panelHeading(o.as);
  const glyph = o.icon ? icoSvg(o.icon) : "";
  const heading = title
    ? `<${h}${attrsHTML({ class: "panel-title", id: o.titleId })}>${title}</${h}>`
    : "";
  const status = o.statusHTML ? `<div class="panel-head-status">${o.statusHTML}</div>` : "";
  const asideEl = aside ? `<div class="panel-head-aside">${aside}</div>` : "";
  const actions = o.actionsHTML ? `<div class="panel-head-actions">${o.actionsHTML}</div>` : "";
  return `<div class="panel-head">${glyph}${heading}${status}${o.headHTML ?? ""}${asideEl}${actions}</div>`;
}

export function panelHTML(o: PanelOptions): string {
  const tag = panelTag(o.tag);
  const size = o.size ?? "md";
  const bodyCls = panelBodyClasses({ flush: o.flush, pad: o.pad, size, cls: o.bodyCls });
  const foot = pick(o.foot, o.footHTML);
  const own: Attrs = { id: o.id, class: panelClasses(o) };
  return (
    `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    panelHeadHTML(o) +
    `<div class="${esc(bodyCls)}"${attrsHTML(o.bodyAttrs)}>${pick(o.body, o.bodyHTML)}</div>` +
    (foot ? `<div class="panel-foot">${foot}</div>` : "") +
    `</${tag}>`
  );
}

export function panelEl(o: PanelOptions): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = panelHTML(o);
  return tpl.content.firstElementChild as HTMLElement;
}

export type SectionHeadLevel = 3 | 4;

export interface SectionHeadToggle {
  id?: string;
  controls: string;
  expanded?: boolean;
  disabled?: boolean;
}

const LEVELS = new Set<number>([3, 4]);

export interface SectionHeadLook {
  level?: SectionHeadLevel;
  cls?: string;
}

export function sectionHeadClasses(look: SectionHeadLook = {}): string {
  const { level = 3, cls } = look;
  if (!LEVELS.has(level)) throw new Error(`section head: no level "${level}"`); // i18n-ignore a programming error
  return ["sec-head", level === 4 && "sec-head-sm", cls].filter(Boolean).join(" ");
}

export interface SectionHeadOptions extends SectionHeadLook {
  title?: string;
  titleHTML?: string;
  id?: string;
  count?: number | string;
  countOpts?: CountOptions;
  toggle?: SectionHeadToggle;
  asideHTML?: string;
  actionsHTML?: string;
  attrs?: Attrs;
}

export function sectionTitleInnerHTML(o: SectionHeadOptions): string {
  const title = pick(o.title, o.titleHTML);
  const count = o.count !== undefined ? countHTML(o.count, o.countOpts) : "";
  if (!o.toggle) return title + count;
  const tg = o.toggle;
  const attrs: Attrs = {
    class: "sec-toggle",
    id: tg.id,
    type: "button",
    "aria-expanded": String(Boolean(tg.expanded)),
    "aria-controls": tg.controls,
    disabled: tg.disabled,
  };
  return `<button${attrsHTML(attrs)}><span>${title}</span>${count}${icoSvg("i-chevron-right")}</button>`;
}

export function sectionHeadHTML(o: SectionHeadOptions): string {
  const h = (o.level ?? 3) === 4 ? "h4" : "h3";
  const own: Attrs = { class: sectionHeadClasses(o) };
  const heading = `<${h}${attrsHTML({ class: "sec-title", id: o.id })}>${sectionTitleInnerHTML(o)}</${h}>`;
  const actions = o.actionsHTML ? `<div class="sec-actions">${o.actionsHTML}</div>` : "";
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${heading}${o.asideHTML ?? ""}${actions}</div>`;
}
