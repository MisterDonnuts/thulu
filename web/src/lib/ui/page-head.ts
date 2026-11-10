// pageHeadHTML() and crumbsHTML() must emit the same markup as PageHead.astro and Crumbs.astro.

import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type PageHeadLevel = "page" | "section";

const LEVELS = new Set<string>(["page", "section"]);

export interface PageHeadLook {
  level?: PageHeadLevel;
  flush?: boolean;
  cls?: string;
}

export function pageHeadClasses(look: PageHeadLook = {}): string {
  const { level = "page", flush = false, cls } = look;
  if (!LEVELS.has(level)) throw new Error(`page head: no level "${level}"`); // i18n-ignore a programming error
  return ["page-head", level === "section" && "page-head-section", flush && "page-head-flush", cls]
    .filter(Boolean)
    .join(" ");
}

export function pageHeadHeading(level: PageHeadLevel = "page"): "h1" | "h2" {
  return level === "section" ? "h2" : "h1";
}

export interface Crumb {
  label: string;
  href?: string;
  id?: string;
  mono?: boolean;
  hideNarrow?: boolean;
  attrs?: Attrs;
}

export type CrumbsSize = "xs" | "sm";

export interface CrumbsLook {
  size?: CrumbsSize;
  line?: boolean;
}

export interface CrumbsOptions extends CrumbsLook {
  label?: string;
  id?: string;
  actionHTML?: string;
}

const CRUMB_SIZES = new Set<string>(["xs", "sm"]);

export function crumbsClasses(look: CrumbsLook = {}): string {
  const { size = "xs", line = false } = look;
  if (!CRUMB_SIZES.has(size)) throw new Error(`crumbs: no size "${size}"`); // i18n-ignore a programming error
  return ["crumbs", size === "sm" && "crumbs-sm", line && "crumbs-line"].filter(Boolean).join(" ");
}

export function crumbClasses(c: Pick<Crumb, "mono" | "hideNarrow">): string {
  return ["crumb", c.mono && "crumb-mono", c.hideNarrow && "crumb-hide-narrow"]
    .filter(Boolean)
    .join(" ");
}

export function crumbsLabel(label?: string): string {
  return label ?? t("ui.labels.breadcrumb");
}

export function checkCrumbs(crumbs: Crumb[]): void {
  crumbs.forEach((c, i) => {
    if (!c.href && i < crumbs.length - 1)
      throw new Error(`crumbs: "${c.label}" needs an href, only the last step may lack one`); // i18n-ignore a programming error
  });
}

export function crumbsHTML(crumbs: Crumb[], o: CrumbsOptions = {}): string {
  checkCrumbs(crumbs);
  const items = crumbs
    .map((c, i) => {
      const sep = i > 0 ? icoSvg("chevron-right", { size: "sm", cls: "crumb-sep" }) : "";
      const step = c.href
        ? `<a${attrsHTML({ href: c.href, id: c.id })}${attrsHTML(c.attrs)}>${esc(c.label)}</a>`
        : `<span${attrsHTML({ id: c.id, "aria-current": "page" })}${attrsHTML(c.attrs)}>${esc(c.label)}</span>`;
      return `<li class="${crumbClasses(c)}">${sep}${step}</li>`;
    })
    .join("");
  const nav: Attrs = { class: crumbsClasses(o), id: o.id, "aria-label": crumbsLabel(o.label) };
  const action = o.actionHTML ? `<span class="crumbs-action">${o.actionHTML}</span>` : "";
  return `<nav${attrsHTML(nav)}><ol class="crumbs-list">${items}</ol>${action}</nav>`;
}

export interface PageBack {
  href: string;
  label: string;
}

export function pageBackHTML(b: PageBack): string {
  return `<a class="page-back" href="${esc(b.href)}">${icoSvg("chevron-right", { cls: "page-back-ico" })}${esc(b.label)}</a>`;
}

export interface PageHeadOptions extends PageHeadLook {
  title: string;
  titleId?: string;
  subtitle?: string;
  subHTML?: string;
  subMax?: number;
  back?: PageBack;
  crumbs?: Crumb[];
  crumbsLabel?: string;
  crumbsId?: string;
  aboveHTML?: string;
  statusHTML?: string;
  actionsHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function pageHeadHTML(o: PageHeadOptions): string {
  const h = pageHeadHeading(o.level);
  const back = o.back ? pageBackHTML(o.back) : "";
  const crumbs = o.crumbs ? crumbsHTML(o.crumbs, { label: o.crumbsLabel, id: o.crumbsId }) : "";
  const heading = `<${h}${attrsHTML({ class: "page-title", id: o.titleId })}>${esc(o.title)}</${h}>`;
  const titleRow = o.statusHTML
    ? `<div class="page-title-row">${heading}<div class="page-head-status">${o.statusHTML}</div></div>`
    : heading;
  const subText = o.subtitle !== undefined ? esc(o.subtitle) : o.subHTML;
  // i18n-ignore an inline CSS length, not words
  const subStyle = o.subMax ? `max-width:${o.subMax}px` : undefined;
  const sub = subText
    ? `<p${attrsHTML({ class: "page-sub", style: subStyle })}>${subText}</p>`
    : "";
  const actions = o.actionsHTML ? `<div class="page-head-actions">${o.actionsHTML}</div>` : "";
  const own: Attrs = { id: o.id, class: pageHeadClasses(o) };
  return `<header${attrsHTML(own)}${attrsHTML(o.attrs)}><div class="page-head-text">${back}${crumbs}${o.aboveHTML ?? ""}${titleRow}${sub}</div>${actions}</header>`;
}
