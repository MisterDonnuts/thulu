// listRowHTML() and groupHeadHTML() must emit the same markup as ListRow.astro and GroupHead.astro.

import { buttonHTML, type ButtonOptions } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type ListRowTag = "div" | "li" | "a" | "button";

export type ListRowSize = "md" | "sm";

const SIZES = new Set<string>(["md", "sm"]);

export type ListRowClamp = 1 | 2;

const CLAMPS = new Set<number>([1, 2]);

export interface ListRowLook {
  size?: ListRowSize;
  wrap?: boolean | "all";
  clamp?: ListRowClamp;
  inline?: boolean;
  boxed?: boolean;
  reveal?: boolean;
  dim?: boolean;
  cls?: string;
}

export function listRowClasses(look: ListRowLook = {}): string {
  const {
    size = "md",
    wrap = false,
    clamp,
    inline = false,
    boxed = false,
    reveal = false,
    dim = false,
    cls,
  } = look;
  if (!SIZES.has(size)) throw new Error(`list-row: no size "${size}"`); // i18n-ignore a programming error
  if (clamp !== undefined && !CLAMPS.has(clamp)) {
    throw new Error(`list-row: no clamp "${String(clamp)}"`); // i18n-ignore a programming error
  }
  if (inline && clamp !== undefined) {
    throw new Error("list-row: an inline meta is one line, it takes no clamp"); // i18n-ignore a programming error
  }
  return [
    "row",
    size === "sm" && "row-sm",
    wrap && "row-wrap",
    wrap === "all" && "row-wrap-all",
    clamp && `row-clamp-${clamp}`,
    inline && "row-inline",
    boxed && "row-boxed",
    reveal && "row-reveal",
    dim && "row-dim",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface ListRowState {
  as?: ListRowTag;
  href?: string;
  current?: boolean | "page";
  expanded?: boolean;
  hasActions?: boolean;
}

export function listRowTag(s: Pick<ListRowState, "as" | "href">): ListRowTag {
  return s.as ?? (s.href ? "a" : "div");
}

export function listRowAttrs(s: ListRowState): Attrs {
  const tag = listRowTag(s);
  if (tag === "a" && !s.href) throw new Error("list-row: a link row needs href"); // i18n-ignore a programming error
  if (tag !== "a" && s.href) throw new Error(`list-row: href on a ${tag} row`); // i18n-ignore a programming error
  if (s.expanded !== undefined && tag !== "button") {
    throw new Error("list-row: only a button row opens a detail"); // i18n-ignore a programming error
  }
  if (s.hasActions && (tag === "a" || tag === "button")) {
    throw new Error(`list-row: actions inside a ${tag} row`); // i18n-ignore a programming error
  }
  return {
    href: s.href,
    type: tag === "button" ? "button" : undefined,
    "aria-current": s.current === true ? "true" : s.current === "page" ? "page" : undefined,
    "aria-expanded": s.expanded === undefined ? undefined : String(s.expanded),
  };
}

export function listRowCaretHTML(): string {
  return `<span class="row-caret">${icoSvg("chevron-right", { size: "sm" })}</span>`;
}

export function listRowTitleClasses(mono = false): string {
  return mono ? "row-title row-title-mono" : "row-title";
}

export interface ListRowOptions extends ListRowLook, ListRowState {
  title?: string;
  titleHTML?: string;
  titleHref?: string;
  titleAttrs?: Attrs;
  mono?: boolean;
  badgesHTML?: string;
  meta?: string;
  metaHTML?: string;
  tagsHTML?: string;
  leadHTML?: string;
  aside?: string;
  asideHTML?: string;
  actions?: ButtonOptions[];
  actionsHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function listRowActionsHTML(actions: ButtonOptions[] = []): string {
  return actions.map((a) => buttonHTML({ ...a, size: "sm" })).join("");
}

export function listRowHTML(o: ListRowOptions): string {
  if (o.title === undefined && o.titleHTML === undefined) {
    throw new Error("list-row: a row needs a title"); // i18n-ignore a programming error
  }
  const actions = listRowActionsHTML(o.actions) + (o.actionsHTML ?? "");
  const tag = listRowTag(o);
  const own: Attrs = {
    id: o.id,
    class: listRowClasses(o),
    ...listRowAttrs({ ...o, hasActions: actions !== "" }),
  };
  const titleInner = o.titleHTML ?? esc(o.title);
  const titleCls = listRowTitleClasses(o.mono);
  const title = o.titleHref
    ? `<a${attrsHTML({ class: titleCls, href: o.titleHref })}${attrsHTML(o.titleAttrs)}>${titleInner}</a>`
    : `<span${attrsHTML({ class: titleCls })}${attrsHTML(o.titleAttrs)}>${titleInner}</span>`;
  const meta = o.metaHTML ?? (o.meta ? esc(o.meta) : "");
  const aside = o.asideHTML ?? (o.aside ? esc(o.aside) : "");
  return (
    `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    (o.expanded !== undefined ? listRowCaretHTML() : "") +
    (o.leadHTML ? `<span class="row-lead">${o.leadHTML}</span>` : "") +
    `<span class="row-main"><span class="row-top">${title}${o.badgesHTML ?? ""}</span>` +
    (meta ? `<span class="row-meta">${meta}</span>` : "") +
    (o.tagsHTML ? `<span class="row-tags">${o.tagsHTML}</span>` : "") +
    `</span>` +
    (aside ? `<span class="row-aside">${aside}</span>` : "") +
    (actions ? `<span class="row-actions">${actions}</span>` : "") +
    `</${tag}>`
  );
}

export function listRowsHTML(
  rows: ListRowOptions[],
  opts: { as?: "div" | "ul"; id?: string; cls?: string; attrs?: Attrs } = {},
): string {
  const tag = opts.as ?? "div";
  const own: Attrs = { id: opts.id, class: ["row-list", opts.cls].filter(Boolean).join(" ") };
  const items = rows.map((r) => listRowHTML(tag === "ul" && !r.as ? { ...r, as: "li" } : r));
  return `<${tag}${attrsHTML(own)}${attrsHTML(opts.attrs)}>${items.join("")}</${tag}>`;
}

export interface RowGroupBoxedLook {
  edge?: boolean;
  cls?: string;
}

export function rowGroupBoxedClasses(look: RowGroupBoxedLook = {}): string {
  return ["row-group-boxed", look.edge && "row-group-boxed-edge", look.cls]
    .filter(Boolean)
    .join(" ");
}

export function rowGroupBoxedHTML(
  innerHTML: string,
  opts: RowGroupBoxedLook & { id?: string; attrs?: Attrs } = {},
): string {
  const own: Attrs = { id: opts.id, class: rowGroupBoxedClasses(opts) };
  return `<div${attrsHTML(own)}${attrsHTML(opts.attrs)}>${innerHTML}</div>`;
}

export type GroupHeadLevel = 2 | 3 | 4;

export interface GroupHeadLook {
  band?: boolean;
  cls?: string;
}

export function groupHeadClasses(look: GroupHeadLook = {}): string {
  return ["group-head", look.band && "group-head-band", look.cls].filter(Boolean).join(" ");
}

const HEADINGS = new Map<number, "h2" | "h3" | "h4">([
  [2, "h2"],
  [3, "h3"],
  [4, "h4"],
]);

export function groupHeadTitleTag(level?: GroupHeadLevel): "span" | "h2" | "h3" | "h4" {
  if (level === undefined) return "span";
  const tag = HEADINGS.get(level);
  if (!tag) throw new Error(`group-head: no heading level ${String(level)}`); // i18n-ignore a programming error
  return tag;
}

export interface GroupHeadOptions extends GroupHeadLook {
  title: string;
  href?: string;
  kicker?: string;
  icon?: string;
  meta?: string;
  extraHTML?: string;
  level?: GroupHeadLevel;
  as?: "div" | "tr";
  cols?: number;
  id?: string;
  attrs?: Attrs;
}

function groupHeadNameHTML(title: string, href: string | undefined): string {
  return href ? `<a class="group-head-link" href="${esc(href)}">${esc(title)}</a>` : esc(title);
}

export function groupHeadHTML(o: GroupHeadOptions): string {
  const tag = groupHeadTitleTag(o.level);
  const kicker = o.kicker ? `<span class="group-head-kicker">${esc(o.kicker)}</span>` : "";
  const inner =
    (o.icon ? icoSvg(o.icon) : "") +
    `<${tag} class="group-head-title">${kicker}${groupHeadNameHTML(o.title, o.href)}</${tag}>` +
    (o.meta ? `<span class="group-head-meta">${esc(o.meta)}</span>` : "") +
    (o.extraHTML ? `<span class="group-head-extra">${o.extraHTML}</span>` : "");
  const cls = groupHeadClasses(o);
  if ((o.as ?? "div") === "tr") {
    const own: Attrs = { id: o.id, class: "group-head-row" };
    return `<tr${attrsHTML(own)}${attrsHTML(o.attrs)}><th${attrsHTML({ colspan: o.cols })}><div class="${cls}">${inner}</div></th></tr>`;
  }
  return `<div${attrsHTML({ id: o.id, class: cls })}${attrsHTML(o.attrs)}>${inner}</div>`;
}
