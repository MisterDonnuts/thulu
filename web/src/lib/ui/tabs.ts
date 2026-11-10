import { replaceURL } from "../page";
import { badgeHTML, dotHTML, type BadgeTone, type DotTone } from "./badge";
import { countHTML } from "./count";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { isHexColor } from "./tag";

export type TabsVariant = "underline" | "pill" | "vertical";

const VARIANTS = new Set<string>(["underline", "pill", "vertical"]);

export interface TabItem {
  id: string;
  label: string;
  labelId?: string;
  icon?: string;
  dot?: boolean | DotTone;
  dotColor?: string;
  badge?: TabBadge;
  count?: number | string;
  flag?: string;
  flagHidden?: boolean;
  disabled?: boolean;
  tabId?: string;
  panelId?: string;
}

export interface TabBadge {
  label: string;
  tone?: BadgeTone;
}

export interface TabsLook {
  variant?: TabsVariant;
  cls?: string;
}

export interface TabsOptions extends TabsLook {
  items: TabItem[];
  active?: string | null;
  label: string;
  id?: string;
  idPrefix?: string;
  panels?: boolean;
  attrs?: Attrs;
}

export function tabsClasses(look: TabsLook = {}): string {
  const { variant = "underline", cls } = look;
  if (!VARIANTS.has(variant)) throw new Error(`tabs: no variant "${variant}"`); // i18n-ignore a programming error
  return ["tabs", variant !== "underline" && `tabs-${variant}`, cls].filter(Boolean).join(" ");
}

export function tabIds(
  item: TabItem,
  o: { idPrefix?: string; panels?: boolean } = {},
): { tab: string; panel?: string; count: string; flag: string } {
  const prefix = o.idPrefix ?? "";
  const tab = item.tabId ?? `${prefix}tab-${item.id}`;
  const panel = item.panelId ?? (o.panels ? `${prefix}panel-${item.id}` : undefined);
  return { tab, panel, count: `${tab}-count`, flag: `${tab}-flag` };
}

export function activeTab(items: TabItem[], active?: string | null): string | undefined {
  if (active === null) return undefined;
  const on = items.find((i) => i.id === active && !i.disabled);
  return (on ?? items.find((i) => !i.disabled))?.id;
}

export function tabStop(items: TabItem[], selected: string | undefined): string | undefined {
  return selected ?? items.find((i) => !i.disabled)?.id;
}

export function tabAttrs(
  item: TabItem,
  selected: boolean,
  o: TabsOptions,
  stop: boolean = selected,
): Attrs {
  const ids = tabIds(item, o);
  return {
    type: "button",
    class: "tab",
    role: "tab",
    id: ids.tab,
    "aria-selected": String(selected),
    "aria-controls": ids.panel,
    tabindex: stop ? undefined : "-1",
    "data-tab": item.id,
    disabled: item.disabled,
  };
}

export function tabDot(item: TabItem): { tone: DotTone; style?: string } {
  const tone: DotTone = typeof item.dot === "string" ? item.dot : "neutral";
  return { tone, style: isHexColor(item.dotColor) ? `--dot-color:${item.dotColor}` : undefined };
}

export function tabInnerHTML(item: TabItem, o: TabsOptions): string {
  const ids = tabIds(item, o);
  let lead = "";
  if (item.dot) {
    const d = tabDot(item);
    lead = dotHTML(d.tone, { attrs: { style: d.style } });
  } else if (item.icon) lead = icoSvg(item.icon, { size: "sm" });
  const badge = item.badge ? badgeHTML(item.badge.label, item.badge.tone) : "";
  const count =
    item.count === undefined
      ? ""
      : countHTML(item.count, { pill: true, hideZero: true, id: ids.count });
  const flag =
    item.flag === undefined
      ? ""
      : `<span class="tab-flag" id="${esc(ids.flag)}"${item.flagHidden ? " hidden" : ""}>${icoSvg("warn")}<span class="visually-hidden">${esc(item.flag)}</span></span>`;
  return `${lead}<span${attrsHTML({ id: item.labelId })}>${esc(item.label)}</span>${badge}${count}${flag}`;
}

export function tablistAttrs(o: TabsOptions): Attrs {
  return {
    id: o.id,
    class: tabsClasses(o),
    role: "tablist",
    "aria-label": o.label,
    "aria-orientation": o.variant === "vertical" ? "vertical" : undefined,
  };
}

export function tabsHTML(o: TabsOptions): string {
  const on = activeTab(o.items, o.active);
  const stop = tabStop(o.items, on);
  const tabs = o.items
    .map(
      (item) =>
        `<button${attrsHTML(tabAttrs(item, item.id === on, o, item.id === stop))}>${tabInnerHTML(item, o)}</button>`,
    )
    .join("");
  return `<div${attrsHTML(tablistAttrs(o))}${attrsHTML(o.attrs)}>${tabs}</div>`;
}

export function tabPanelAttrs(
  item: TabItem,
  selected: boolean,
  o: { idPrefix?: string; panels?: boolean } = {},
): Attrs {
  const ids = tabIds(item, { ...o, panels: true });
  return {
    id: ids.panel,
    role: "tabpanel",
    "aria-labelledby": ids.tab,
    hidden: !selected,
  };
}

export interface BindTabsOptions {
  hash?: boolean;
  beforeChange?: (id: string) => boolean | Promise<boolean>;
  onChange?: (id: string, how: { user: boolean }) => void;
  signal?: AbortSignal;
}

export interface TabsHandle {
  select: (id: string, opts?: { focus?: boolean }) => void;
  current: () => string | undefined;
}

export function tabFromHash(hash: string, ids: string[]): string | undefined {
  const h = hash.replace(/^#/, "");
  return ids.includes(h) ? h : undefined;
}

export function tabKeyTarget(key: string, i: number, count: number, vertical = false): number {
  const next = key === "ArrowRight" || (vertical && key === "ArrowDown");
  const prev = key === "ArrowLeft" || (vertical && key === "ArrowUp");
  if (next) return (i + 1) % count;
  if (prev) return (i - 1 + count) % count;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  return -1;
}

export function bindTabs(list: HTMLElement, o: BindTabsOptions = {}): TabsHandle {
  const tabs = Array.from(list.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
  const ids = tabs.map((b) => b.dataset.tab ?? "");
  const vertical = list.getAttribute("aria-orientation") === "vertical";
  const enabled = () => tabs.filter((b) => !b.disabled);
  let current = tabs.find((b) => b.getAttribute("aria-selected") === "true")?.dataset.tab;

  function paint(id: string, focus: boolean) {
    const on = tabs.find((b) => b.dataset.tab === id);
    if (!on || on.disabled) return false;
    current = id;
    const shown = on.getAttribute("aria-controls");
    for (const b of tabs) {
      const sel = b === on;
      b.setAttribute("aria-selected", String(sel));
      b.tabIndex = sel ? 0 : -1;
      const panel = b.getAttribute("aria-controls");
      if (panel && panel !== shown) {
        const el = list.ownerDocument.getElementById(panel);
        if (el) el.hidden = true;
      }
    }
    if (shown) {
      const el = list.ownerDocument.getElementById(shown);
      if (el) el.hidden = false;
    }
    on.scrollIntoView({ block: "nearest", inline: "nearest" });
    if (focus) on.focus();
    return true;
  }

  function writeHash(id: string) {
    const { pathname, search } = window.location;
    replaceURL(id === ids[0] ? pathname + search : `${pathname}${search}#${id}`);
  }

  function commit(id: string, focus: boolean) {
    if (id === current || !paint(id, focus)) return;
    if (o.hash) writeHash(id);
    o.onChange?.(id, { user: true });
  }

  function pick(id: string, focus: boolean) {
    if (id === current) {
      if (focus) tabs.find((b) => b.dataset.tab === id)?.focus();
      return;
    }
    const target = tabs.find((b) => b.dataset.tab === id);
    if (!target || target.disabled) return;
    if (!o.beforeChange) {
      commit(id, focus);
      return;
    }
    void Promise.resolve(o.beforeChange(id)).then((ok) => {
      if (ok) commit(id, focus);
    });
  }

  for (const b of tabs) {
    b.addEventListener("click", () => pick(b.dataset.tab ?? "", false));
    b.addEventListener("keydown", (e) => {
      const live = enabled();
      const j = tabKeyTarget(e.key, live.indexOf(b), live.length, vertical);
      if (j < 0) return;
      e.preventDefault();
      pick(live[j].dataset.tab ?? "", true);
    });
  }

  if (o.hash) {
    const fromHash = () => tabFromHash(window.location.hash, ids) ?? ids[0];
    const start = fromHash();
    if (start && start !== current && paint(start, false)) o.onChange?.(start, { user: false });
    window.addEventListener(
      "hashchange",
      () => {
        const id = fromHash();
        if (id !== current && paint(id, false)) o.onChange?.(id, { user: false });
      },
      { signal: o.signal },
    );
  }

  return {
    select: (id, opts = {}) => {
      if (id !== current && paint(id, opts.focus ?? false)) o.onChange?.(id, { user: false });
      else if (opts.focus) tabs.find((b) => b.dataset.tab === id)?.focus();
    },
    current: () => current,
  };
}
