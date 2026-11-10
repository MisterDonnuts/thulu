// Keep in sync with Drawer.astro and styles/primitives/drawer.css.

import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { buttonHTML, type IconButtonOptions } from "./button";

export {
  bindDrawer,
  bindDrawerResize,
  clampDrawerWidth,
  type BindDrawerOpts,
  type DrawerControls,
  type DrawerResizeOpts,
} from "../drawer";

export type DrawerSide = "right" | "left" | "bottom";

export type DrawerSize = "sm" | "md" | "lg";

export type DrawerBody = "padded" | "flush" | "frame";

const SIDES = new Set<string>(["right", "left", "bottom"]);
const SIZES = new Set<string>(["sm", "md", "lg"]);
const BODIES = new Set<string>(["padded", "flush", "frame"]);

export interface DrawerLook {
  side?: DrawerSide;
  size?: DrawerSize;
  modal?: boolean;
  cls?: string;
}

export function drawerClasses(look: DrawerLook = {}): string {
  const { side = "right", size, modal = true, cls } = look;
  if (!SIDES.has(side)) throw new Error(`drawer: no side "${side}"`); // i18n-ignore a programming error
  if (size !== undefined && !SIZES.has(size)) throw new Error(`drawer: no size "${size}"`); // i18n-ignore a programming error
  if (side === "bottom" && size !== undefined) {
    throw new Error("drawer: a bottom sheet has no size"); // i18n-ignore a programming error
  }
  const width = side === "bottom" ? undefined : `drawer-${size ?? "md"}`;
  return ["drawer", `drawer-${side}`, width, !modal && "drawer-docked", cls]
    .filter(Boolean)
    .join(" ");
}

export function drawerOverlayClasses(side: DrawerSide = "right"): string {
  return ["overlay", "overlay-drawer", side !== "right" && `overlay-drawer-${side}`]
    .filter(Boolean)
    .join(" ");
}

export function drawerBodyClasses(body: DrawerBody = "padded", extra?: string): string {
  if (!BODIES.has(body)) throw new Error(`drawer: no body "${body}"`); // i18n-ignore a programming error
  return ["drawer-body", body !== "padded" && `drawer-body-${body}`, extra]
    .filter(Boolean)
    .join(" ");
}

export function drawerIds(id: string, titleId?: string): { title: string; sub: string } {
  return { title: titleId ?? `${id}-title`, sub: `${id}-sub` };
}

// data-modal-close is the hook bindDrawer (lib/drawer.ts) wires for modal and docked drawers.
export function drawerCloseButton(): IconButtonOptions {
  return {
    iconOnly: true,
    icon: "x",
    size: "lg",
    label: t("common.buttons.close"),
    attrs: { "data-modal-close": "" },
  };
}

export function drawerBackButton(label?: string): IconButtonOptions {
  return {
    iconOnly: true,
    icon: "chevron-left",
    size: "lg",
    label: label ?? t("common.buttons.back"),
  };
}

export function drawerResizerAttrs(): Attrs {
  return {
    class: "drawer-resizer",
    role: "separator",
    "aria-orientation": "vertical",
    "aria-label": t("ui.drawer.resize"),
    title: t("ui.drawer.resize_hint"),
    tabindex: "0",
  };
}

export function checkResizable(side: DrawerSide, resizable: boolean | undefined): void {
  if (resizable && side === "bottom") {
    throw new Error("drawer: a bottom sheet cannot be resizable"); // i18n-ignore a programming error
  }
}

export interface DrawerHeadOptions {
  title: string;
  titleId: string;
  titleAttrs?: Attrs;
  sub?: string;
  subHTML?: string;
  subId?: string;
  closable?: boolean;
  leadHTML?: string;
  eyebrowHTML?: string;
  actionsHTML?: string;
  headHTML?: string;
}

export function drawerHeadHTML(o: DrawerHeadOptions): string {
  const lead = o.leadHTML ? `<div class="drawer-lead">${o.leadHTML}</div>` : "";
  const eyebrow = o.eyebrowHTML ? `<div class="drawer-eyebrow">${o.eyebrowHTML}</div>` : "";
  const subInner = o.subHTML ?? (o.sub !== undefined ? esc(o.sub) : undefined);
  const sub =
    subInner !== undefined
      ? `<p class="drawer-sub"${attrsHTML({ id: o.subId })}>${subInner}</p>`
      : "";
  const h2 = `<h2 class="drawer-title" id="${esc(o.titleId)}"${attrsHTML(o.titleAttrs)}>${esc(o.title)}</h2>`;
  const heading = `<div class="drawer-heading">${eyebrow}${h2}${sub}</div>`;
  const actions = o.actionsHTML ? `<div class="drawer-actions">${o.actionsHTML}</div>` : "";
  const close = (o.closable ?? true) ? buttonHTML(drawerCloseButton()) : "";
  return `<header class="drawer-head"><div class="drawer-head-row">${lead}${heading}${actions}${close}</div>${o.headHTML ?? ""}</header>`;
}

export interface DrawerOptions extends DrawerLook {
  id: string;
  title: string;
  titleId?: string;
  titleAttrs?: Attrs;
  sub?: string;
  closable?: boolean;
  resizable?: boolean;
  body?: DrawerBody;
  bodyClass?: string;
  attrs?: Attrs;
  leadHTML?: string;
  eyebrowHTML?: string;
  actionsHTML?: string;
  headHTML?: string;
  barHTML?: string;
  bodyHTML?: string;
  footNote?: string;
  footHTML?: string;
}

export function drawerHTML(o: DrawerOptions): string {
  const side = o.side ?? "right";
  const modal = o.modal ?? true;
  checkResizable(side, o.resizable);
  const ids = drawerIds(o.id, o.titleId);
  const box: Attrs = {
    id: modal ? undefined : o.id,
    class: drawerClasses({ side, size: o.size, modal, cls: o.cls }),
    role: "dialog",
    "aria-modal": modal ? "true" : undefined,
    "aria-labelledby": ids.title,
    "aria-describedby": o.sub !== undefined ? ids.sub : undefined,
    tabindex: "-1",
  };

  const resizer = o.resizable ? `<div${attrsHTML(drawerResizerAttrs())}></div>` : "";
  const head = drawerHeadHTML({
    title: o.title,
    titleId: ids.title,
    titleAttrs: o.titleAttrs,
    sub: o.sub,
    subId: o.sub !== undefined ? ids.sub : undefined,
    closable: o.closable,
    leadHTML: o.leadHTML,
    eyebrowHTML: o.eyebrowHTML,
    actionsHTML: o.actionsHTML,
    headHTML: o.headHTML,
  });
  const bar = o.barHTML ? `<div class="drawer-bar">${o.barHTML}</div>` : "";
  const body = `<div class="${esc(drawerBodyClasses(o.body, o.bodyClass))}">${o.bodyHTML ?? ""}</div>`;
  const note = o.footNote ? `<p class="drawer-foot-note">${esc(o.footNote)}</p>` : "";
  const foot =
    note || o.footHTML ? `<footer class="drawer-foot">${note}${o.footHTML ?? ""}</footer>` : "";

  const inner = `<aside${attrsHTML(box)}${modal ? "" : attrsHTML(o.attrs)}>${resizer}${head}${bar}${body}${foot}</aside>`;
  if (!modal) return inner;
  const overlay: Attrs = { id: o.id, class: drawerOverlayClasses(side), role: "presentation" };
  return `<div${attrsHTML(overlay)}${attrsHTML(o.attrs)}>${inner}</div>`;
}

export function drawerEl(o: DrawerOptions): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = drawerHTML(o);
  const el = tpl.content.firstElementChild as HTMLElement;
  document.body.appendChild(el);
  return el;
}
