// menuHTML() and menuItemHTML() must emit the same markup as Menu.astro and MenuItem.astro.

import { dotHTML } from "./badge";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { kbdHTML, keyShortcuts } from "./kbd";
import { isHexColor } from "./tag";

export type MenuDensity = "compact" | "rich";
export type MenuAlign = "end" | "start";
export type MenuItemRole = "menuitem" | "menuitemradio" | "menuitemcheckbox";

const DENSITIES = new Set<string>(["compact", "rich"]);
const ALIGNS = new Set<string>(["end", "start"]);

export interface MenuItem {
  title: string;
  sub?: string;
  icon?: string;
  dot?: string;
  leadHTML?: string;
  indent?: boolean;
  hint?: string;
  kbd?: string | string[];
  key?: string;
  danger?: boolean;
  disabled?: boolean;
  checked?: boolean;
  role?: MenuItemRole;
  href?: string;
  keepOpen?: boolean;
  hidden?: boolean;
  id?: string;
  titleId?: string;
  subId?: string;
  cls?: string;
  attrs?: Attrs;
}

export interface MenuSep {
  sep: true;
  hidden?: boolean;
}
export interface MenuHead {
  head: string;
  sub?: string;
  leadHTML?: string;
  id?: string;
}
export interface MenuNote {
  note: string;
  id?: string;
  hidden?: boolean;
}
export interface MenuSlot {
  slot: string;
  html?: string;
}
export type MenuEntry = MenuItem | MenuSep | MenuHead | MenuNote | MenuSlot;

export const isMenuSep = (e: MenuEntry): e is MenuSep => "sep" in e;
export const isMenuHead = (e: MenuEntry): e is MenuHead => "head" in e;
export const isMenuNote = (e: MenuEntry): e is MenuNote => "note" in e;
export const isMenuSlot = (e: MenuEntry): e is MenuSlot => "slot" in e;
export const isMenuItem = (e: MenuEntry): e is MenuItem =>
  !isMenuSep(e) && !isMenuHead(e) && !isMenuNote(e) && !isMenuSlot(e);

export interface MenuLook {
  density?: MenuDensity;
  align?: MenuAlign;
  floating?: boolean;
  cls?: string;
}

export function menuClasses(look: MenuLook = {}): string {
  const { density = "compact", align = "end", floating = false, cls } = look;
  if (!DENSITIES.has(density)) throw new Error(`menu: no density "${density}"`); // i18n-ignore a programming error
  if (!ALIGNS.has(align)) throw new Error(`menu: no align "${align}"`); // i18n-ignore a programming error
  return [
    "menu",
    density === "rich" && "menu-rich",
    align === "start" && "menu-start",
    floating && "menu-floating",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface MenuA11y {
  id?: string;
  label?: string;
  labelledBy?: string;
  open?: boolean;
}

export function menuAttrs(a: MenuA11y): Attrs {
  if (!a.label && !a.labelledBy) {
    throw new Error("menu: needs a label or labelledBy"); // i18n-ignore a programming error
  }
  return {
    id: a.id,
    role: "menu",
    "aria-label": a.label,
    "aria-labelledby": a.labelledBy,
    tabindex: "-1",
    hidden: !a.open,
  };
}

export interface MenuOptions extends MenuLook, MenuA11y {
  attrs?: Attrs;
}

export function menuHTML(entries: MenuEntry[], opts: MenuOptions): string {
  const own = { class: menuClasses(opts), ...menuAttrs(opts) };
  return `<div${attrsHTML(own)}${attrsHTML(opts.attrs)}>${menuItemsHTML(entries)}</div>`;
}

export function menuItemsHTML(entries: MenuEntry[]): string {
  return entries.map(menuEntryHTML).join("");
}

function menuEntryHTML(e: MenuEntry): string {
  if (isMenuSep(e)) return `<div${attrsHTML(menuSepAttrs(e))}></div>`;
  if (isMenuNote(e)) {
    return `<p${attrsHTML(menuNoteAttrs(e))}>${esc(e.note)}</p>`;
  }
  if (isMenuSlot(e)) return `<div class="menu-slot">${e.html ?? ""}</div>`;
  if (isMenuHead(e)) {
    const sub = e.sub ? `<span class="menu-head-sub">${esc(e.sub)}</span>` : "";
    return `<div${attrsHTML({ id: e.id, class: "menu-head" })}>${e.leadHTML ?? ""}<span class="menu-head-body"><span class="menu-head-title">${esc(e.head)}</span>${sub}</span></div>`;
  }
  return menuItemHTML(e);
}

export function menuSepAttrs(e: MenuSep): Attrs {
  return { class: "menu-sep", role: "separator", hidden: e.hidden };
}
export function menuNoteAttrs(e: MenuNote): Attrs {
  return { id: e.id, class: "menu-note", role: "note", hidden: e.hidden };
}

export function menuItemRole(item: Pick<MenuItem, "role" | "checked">): MenuItemRole {
  return item.role ?? (item.checked === undefined ? "menuitem" : "menuitemradio");
}

export function menuItemClasses(item: Pick<MenuItem, "danger" | "cls">): string {
  return ["menu-item", item.danger && "menu-item-danger", item.cls].filter(Boolean).join(" ");
}

export function menuItemTag(item: Pick<MenuItem, "href">): "a" | "button" {
  return item.href ? "a" : "button";
}

export function menuItemAttrs(item: MenuItem): Attrs {
  const role = menuItemRole(item);
  const choice = role !== "menuitem";
  if (!choice && item.checked !== undefined) {
    throw new Error("menu: a checked item needs role menuitemradio or menuitemcheckbox"); // i18n-ignore a programming error
  }
  const tag = menuItemTag(item);
  return {
    id: item.id,
    type: tag === "button" ? "button" : undefined,
    href: tag === "a" && !item.disabled ? item.href : undefined,
    role,
    "aria-checked": choice ? String(!!item.checked) : undefined,
    "aria-disabled": item.disabled ? "true" : undefined,
    "aria-keyshortcuts": keyShortcuts(item.kbd),
    tabindex: "-1",
    "data-key": item.key,
    "data-keep-open": item.keepOpen ? "" : undefined,
    hidden: item.hidden,
  };
}

export function menuItemLead(
  item: Pick<MenuItem, "leadHTML" | "icon" | "dot" | "indent">,
): "html" | "icon" | "dot" | "empty" | null {
  if (item.leadHTML) return "html";
  if (item.icon) return "icon";
  if (item.dot) return "dot";
  return item.indent ? "empty" : null;
}

export function menuDotStyle(dot: string | undefined): string | undefined {
  return isHexColor(dot) ? `--dot-color:${dot}` : undefined;
}

export function menuItemHTML(item: MenuItem): string {
  const tag = menuItemTag(item);
  const own = { class: menuItemClasses(item), ...menuItemAttrs(item) };
  const kind = menuItemLead(item);
  let lead = "";
  if (kind === "html") lead = item.leadHTML ?? "";
  else if (kind === "icon") lead = icoSvg(item.icon ?? "");
  else if (kind === "dot") lead = dotHTML("neutral", { attrs: { style: menuDotStyle(item.dot) } });
  const leadBox = kind ? `<span class="menu-icon">${lead}</span>` : "";
  const sub = item.sub
    ? `<span${attrsHTML({ id: item.subId, class: "menu-sub" })}>${esc(item.sub)}</span>`
    : "";
  const hint = item.hint ? `<span class="menu-hint">${esc(item.hint)}</span>` : "";
  const kbd = item.kbd ? kbdHTML(item.kbd, { size: "sm", cls: "menu-kbd", decorative: true }) : "";
  const check = item.checked ? icoSvg("check", { cls: "menu-check" }) : "";
  const title = `<span${attrsHTML({ id: item.titleId, class: "menu-title" })}>${esc(item.title)}</span>`;
  return `<${tag}${attrsHTML(own)}${attrsHTML(item.attrs)}>${leadBox}<span class="menu-body">${title}${sub}</span>${hint}${kbd}${check}</${tag}>`;
}

export interface Rect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export function menuPosition(
  anchor: Rect,
  menu: { width: number; height: number },
  viewport: { width: number; height: number },
  align: MenuAlign = "end",
): { left: number; top: number } {
  const pad = 8;
  let left: number;
  if (align === "end") {
    left = anchor.right - menu.width;
    if (left < pad) left = anchor.left;
  } else {
    left = anchor.left;
    if (left + menu.width > viewport.width - pad) left = anchor.right - menu.width;
  }
  left = Math.max(pad, Math.min(left, viewport.width - menu.width - pad));
  let top = anchor.bottom + 4;
  if (top + menu.height > viewport.height - pad) top = anchor.top - menu.height - 4;
  top = Math.max(pad, Math.min(top, viewport.height - menu.height - pad));
  return { left, top };
}

// A transformed ancestor (the task drawer slides in with one) moves where fixed (0, 0) lands,
// so the box is measured at 0,0 first and that offset subtracted.
export function floatAt(el: HTMLElement, anchor: Rect, align: MenuAlign = "end"): void {
  el.style.setProperty("--float-x", "0px");
  el.style.setProperty("--float-y", "0px");
  const origin = el.getBoundingClientRect();
  const size = { width: el.offsetWidth, height: el.offsetHeight };
  const viewport = { width: window.innerWidth, height: window.innerHeight };
  const { left, top } = menuPosition(anchor, size, viewport, align);
  el.style.setProperty("--float-x", `${Math.round(left - origin.left)}px`);
  el.style.setProperty("--float-y", `${Math.round(top - origin.top)}px`);
}

let openLayer: { el: HTMLElement; dismiss: (restore: boolean) => void } | null = null;

// Escape is caught in the capture phase and stopped, so a drawer or modal under the popover stays
// open.
export function dismissLayer(
  el: HTMLElement,
  anchor: HTMLElement | null,
  dismiss: (restore: boolean) => void,
): () => void {
  if (openLayer && openLayer.el !== el) openLayer.dismiss(false);
  const ctl = new AbortController();
  const layer = { el, dismiss };
  openLayer = layer;
  document.addEventListener(
    "pointerdown",
    (e) => {
      const target = e.target as Node;
      if (el.contains(target) || anchor?.contains(target)) return;
      dismiss(false);
    },
    { capture: true, signal: ctl.signal },
  );
  document.addEventListener(
    "keydown",
    (e) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopImmediatePropagation();
      dismiss(true);
    },
    { capture: true, signal: ctl.signal },
  );
  return () => {
    ctl.abort();
    if (openLayer === layer) openLayer = null;
  };
}

export const MENU_ROW = "[data-menu-row]";

function menuItemsInView(menu: HTMLElement): HTMLElement[] {
  return Array.from(menu.querySelectorAll<HTMLElement>('[role^="menuitem"]')).filter((el) => {
    const hiddenAt = el.closest("[hidden]");
    return !hiddenAt || !menu.contains(hiddenAt);
  });
}

function rowOf(menu: HTMLElement, el: Element): HTMLElement | null {
  const row = el.closest<HTMLElement>(MENU_ROW);
  return row && menu.contains(row) ? row : null;
}

export function menuFocusables(menu: HTMLElement): HTMLElement[] {
  const all = menuItemsInView(menu);
  const active = document.activeElement;
  return all.filter((el) => {
    const row = rowOf(menu, el);
    if (!row) return true;
    const choices = all.filter((x) => row.contains(x));
    const stop =
      choices.find((x) => x === active) ??
      choices.find((x) => x.getAttribute("aria-checked") === "true") ??
      choices[0];
    return el === stop;
  });
}

function rowStep(e: KeyboardEvent, menu: HTMLElement): boolean {
  if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return false;
  const active = document.activeElement as HTMLElement | null;
  const row = active ? rowOf(menu, active) : null;
  if (!active || !row) return false;
  const choices = menuItemsInView(menu).filter((x) => row.contains(x));
  const at = choices.indexOf(active);
  const to = (at + (e.key === "ArrowRight" ? 1 : -1) + choices.length) % choices.length;
  e.preventDefault();
  e.stopPropagation();
  choices[to]?.focus();
  return true;
}

export function menuStep(e: KeyboardEvent, menu: HTMLElement): boolean {
  if (rowStep(e, menu)) return true;
  const items = menuFocusables(menu);
  if (!items.length) return false;
  const at = items.indexOf(document.activeElement as HTMLElement);
  let to = -1;
  if (e.key === "ArrowDown") to = (at + 1) % items.length;
  else if (e.key === "ArrowUp") to = at <= 0 ? items.length - 1 : at - 1;
  else if (e.key === "Home") to = 0;
  else if (e.key === "End") to = items.length - 1;
  if (to < 0) return false;
  e.preventDefault();
  e.stopPropagation();
  items[to]?.focus();
  return true;
}

export function menuKeyOf(e: Pick<KeyboardEvent, "key">): string {
  if (e.key === "Delete" || e.key === "Backspace") return "delete";
  return e.key.length === 1 ? e.key.toLowerCase() : "";
}

export interface BindMenuOptions {
  // The menu closes (focus back on the trigger) before onSelect runs, so a dialog the item opens
  // can take the focus.
  onSelect?: (item: HTMLElement, e: Event) => void;
  onOpen?: () => void;
  onClose?: () => void;
  focus?: "first" | "checked";
  signal?: AbortSignal;
}

export interface MenuHandle {
  open(at?: { x: number; y: number }, focus?: "first" | "last"): void;
  close(restoreFocus?: boolean): void;
  toggle(): void;
  isOpen(): boolean;
}

export function bindMenu(
  menu: HTMLElement,
  trigger: HTMLElement,
  opts: BindMenuOptions = {},
): MenuHandle {
  const signal = opts.signal;
  if (!trigger.hasAttribute("aria-haspopup")) trigger.setAttribute("aria-haspopup", "menu");
  if (menu.id && !trigger.hasAttribute("aria-controls")) {
    trigger.setAttribute("aria-controls", menu.id);
  }
  trigger.setAttribute("aria-expanded", "false");
  let release: (() => void) | null = null;
  let point: { x: number; y: number } | undefined;
  let follow: AbortController | null = null;

  const floating = () => menu.classList.contains("menu-floating");
  const align = (): MenuAlign => (menu.classList.contains("menu-start") ? "start" : "end");
  const place = () => {
    if (!floating()) return;
    const r = point
      ? { left: point.x, top: point.y, right: point.x, bottom: point.y }
      : trigger.getBoundingClientRect();
    floatAt(menu, r, align());
  };

  const handle: MenuHandle = {
    isOpen: () => !menu.hidden,
    open(at, focus = "first") {
      if (!menu.hidden) handle.close(false);
      point = at;
      opts.onOpen?.();
      menu.hidden = false;
      trigger.setAttribute("aria-expanded", "true");
      place();
      release = dismissLayer(menu, trigger, (restore) => handle.close(restore));
      follow = new AbortController();
      window.addEventListener("resize", place, { signal: follow.signal });
      window.addEventListener("scroll", place, {
        signal: follow.signal,
        capture: true,
        passive: true,
      });
      const items = menuFocusables(menu);
      const checked =
        opts.focus === "checked"
          ? items.find((el) => el.getAttribute("aria-checked") === "true")
          : undefined;
      (focus === "last" ? items.at(-1) : (checked ?? items.at(0)))?.focus();
    },
    close(restoreFocus = true) {
      if (menu.hidden) return;
      const hadFocus = menu.contains(document.activeElement);
      menu.hidden = true;
      trigger.setAttribute("aria-expanded", "false");
      release?.();
      release = null;
      follow?.abort();
      follow = null;
      if (restoreFocus && hadFocus) trigger.focus();
      opts.onClose?.();
    },
    toggle() {
      if (menu.hidden) handle.open();
      else handle.close();
    },
  };

  trigger.addEventListener("click", () => handle.toggle(), { signal });
  trigger.addEventListener(
    "keydown",
    (e) => {
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      e.preventDefault();
      handle.open(undefined, e.key === "ArrowUp" ? "last" : "first");
    },
    { signal },
  );
  menu.addEventListener(
    "keydown",
    (e) => {
      if (menuStep(e, menu)) return;
      if (e.key === "Tab") {
        handle.close(false);
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const key = menuKeyOf(e);
      if (!key) return;
      const hit = menuFocusables(menu).find((el) => el.dataset.key === key);
      if (!hit) return;
      e.preventDefault();
      e.stopPropagation();
      hit.click();
    },
    { signal },
  );
  menu.addEventListener(
    "click",
    (e) => {
      const item = (e.target as HTMLElement).closest<HTMLElement>('[role^="menuitem"]');
      if (!item || !menu.contains(item)) return;
      if (item.getAttribute("aria-disabled") === "true") {
        e.preventDefault();
        return;
      }
      if (!item.hasAttribute("data-keep-open")) handle.close(true);
      opts.onSelect?.(item, e);
    },
    { signal },
  );
  return handle;
}
