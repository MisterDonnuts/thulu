// pickerHTML() must emit the same markup as Picker.astro.

import { t } from "../../i18n";
import { dotHTML } from "./badge";
import { buttonHTML, type IconButtonOptions } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { dismissLayer, floatAt, menuDotStyle } from "./menu";
import { tagHTML, tagRemoveLabel, type TagOptions } from "./tag";

export type PickerPlace = "prop" | "floating" | "inline";
export type PickerWidth = "sm" | "md" | "lg";

const PLACES = new Set<string>(["prop", "floating", "inline"]);
const WIDTHS = new Set<string>(["sm", "md", "lg"]);

export interface PickerItem {
  value: string;
  label: string;
  labelHTML?: string;
  hint?: string;
  icon?: string;
  dot?: string;
  leadHTML?: string;
  selected?: boolean;
  disabled?: boolean;
  chip?: Pick<TagOptions, "color" | "dot" | "tone">;
  group?: string;
  attrs?: Attrs;
}

export interface PickerCurrentItem {
  value: string;
  label: string;
  labelHTML?: string;
  hint?: string;
}

export interface PickerCurrent {
  heading: string;
  items: PickerCurrentItem[];
  empty: string;
}

export interface PickerKind {
  label: string;
  options: Array<{ value: string; label: string; disabled?: boolean }>;
  value: string;
}

export interface PickerLook {
  place?: PickerPlace;
  width?: PickerWidth;
  start?: boolean;
  up?: boolean;
  cls?: string;
}

export function pickerClasses(look: PickerLook = {}): string {
  const { place = "prop", width, start = false, up = false, cls } = look;
  if (!PLACES.has(place)) throw new Error(`picker: no place "${place}"`); // i18n-ignore a programming error
  if (width && !WIDTHS.has(width)) throw new Error(`picker: no width "${width}"`); // i18n-ignore a programming error
  return [
    "nselect",
    place === "floating" && "nselect-floating",
    place === "inline" && "nselect-inline",
    start && "nselect-start",
    up && "nselect-up",
    width && `nselect-${width}`,
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface PickerState {
  id: string;
  label: string;
  items: PickerItem[];
  multi?: boolean;
  search?: string;
  query?: string;
  chips?: boolean;
  hint?: string;
  create?: string;
  createHTML?: string;
  empty?: string;
  active?: number;
  current?: PickerCurrent;
  kind?: PickerKind;
  errors?: boolean;
}

export interface PickerOptions extends PickerLook, PickerState {
  attrs?: Attrs;
}

export const pickerListId = (id: string): string => `${id}-list`;
export const pickerInputId = (id: string): string => `${id}-q`;
export const pickerOptionId = (id: string, i: number): string => `${id}-o${i}`;
export const pickerCreateId = (id: string): string => `${id}-create`;
export const pickerGroupId = (id: string, n: number): string => `${id}-g${n}`;
export const pickerCurrentId = (id: string): string => `${id}-current`;
export const pickerKindId = (id: string): string => `${id}-kind`;
export const pickerErrorId = (id: string): string => `${id}-error`;

export function pickerActiveId(s: PickerState): string | undefined {
  const a = s.active ?? -1;
  if (a >= 0 && a < s.items.length) return pickerOptionId(s.id, a);
  if (a === s.items.length && hasCreate(s)) return pickerCreateId(s.id);
  return undefined;
}

const hasCreate = (s: Pick<PickerState, "create" | "createHTML">) =>
  Boolean(s.create) || Boolean(s.createHTML);

export function pickerAttrs(s: Pick<PickerState, "id" | "label">): Attrs {
  if (!s.id || !s.label) throw new Error("picker: needs an id and a label"); // i18n-ignore a programming error
  return { id: s.id, "data-picker": "" };
}

export function pickerInputAttrs(s: PickerState): Attrs {
  return {
    id: pickerInputId(s.id),
    class: "nselect-input",
    type: "text",
    role: "combobox",
    "aria-expanded": "true",
    "aria-controls": pickerListId(s.id),
    "aria-autocomplete": "list",
    "aria-activedescendant": pickerActiveId(s),
    "aria-label": s.label,
    placeholder: s.search,
    value: s.query === "" ? undefined : s.query,
    autocomplete: "off",
    spellcheck: "false",
  };
}

export function pickerListAttrs(s: PickerState): Attrs {
  const own = s.search === undefined;
  return {
    id: pickerListId(s.id),
    class: "nselect-list",
    role: "listbox",
    "aria-label": s.label,
    "aria-multiselectable": s.multi ? "true" : undefined,
    tabindex: own ? "0" : undefined,
    "aria-activedescendant": own ? pickerActiveId(s) : undefined,
  };
}

export function pickerOptionAttrs(s: PickerState, item: PickerItem, i: number): Attrs {
  return {
    id: pickerOptionId(s.id, i),
    class: i === (s.active ?? -1) ? "nselect-item active" : "nselect-item",
    role: "option",
    "aria-selected": String(!!item.selected),
    "aria-disabled": item.disabled ? "true" : undefined,
    "data-value": item.value,
    "data-index": String(i),
  };
}

export function pickerCreateAttrs(s: PickerState): Attrs {
  return {
    id: pickerCreateId(s.id),
    class:
      s.active === s.items.length
        ? "nselect-item nselect-create active"
        : "nselect-item nselect-create",
    role: "option",
    "aria-selected": "false",
    "data-create": "",
  };
}

export function pickerLead(
  item: Pick<PickerItem, "leadHTML" | "icon" | "dot">,
): "html" | "icon" | "dot" | null {
  if (item.leadHTML) return "html";
  if (item.icon) return "icon";
  return item.dot ? "dot" : null;
}

export function pickerChipOptions(item: PickerItem): TagOptions {
  return { ...item.chip, removable: { attrs: { "data-remove": item.value } } };
}

export function pickerEmptyText(s: Pick<PickerState, "empty">): string {
  return s.empty ?? t("ui.picker.no_matches");
}

function optionHTML(s: PickerState, item: PickerItem, i: number): string {
  const kind = pickerLead(item);
  let lead = "";
  if (kind === "html") lead = item.leadHTML ?? "";
  else if (kind === "icon") lead = icoSvg(item.icon ?? "");
  else if (kind === "dot") lead = dotHTML("neutral", { attrs: { style: menuDotStyle(item.dot) } });
  const leadBox = kind ? `<span class="nselect-lead">${lead}</span>` : "";
  const label = item.labelHTML ?? esc(item.label);
  const hint = item.hint ? `<span class="nselect-item-hint">${esc(item.hint)}</span>` : "";
  const check = item.selected ? icoSvg("check", { cls: "nselect-check" }) : "";
  return `<div${attrsHTML(pickerOptionAttrs(s, item, i))}${attrsHTML(item.attrs)}>${leadBox}<span class="nselect-label">${label}</span>${hint}${check}</div>`;
}

export interface PickerGroup {
  label?: string;
  start: number;
  items: PickerItem[];
}

export function pickerGroups(items: PickerItem[]): PickerGroup[] {
  const out: PickerGroup[] = [];
  items.forEach((item, i) => {
    const last = out.at(-1);
    if (last && last.label === item.group) last.items.push(item);
    else out.push({ label: item.group, start: i, items: [item] });
  });
  return out;
}

export function pickerGroupAttrs(id: string, n: number): Attrs {
  return { class: "nselect-group", role: "group", "aria-labelledby": pickerGroupId(id, n) };
}

export function pickerListHTML(s: PickerState): string {
  const options = pickerGroups(s.items)
    .map((g, n) => {
      const opts = g.items.map((item, k) => optionHTML(s, item, g.start + k)).join("");
      if (g.label === undefined) return opts;
      const head = `<div${attrsHTML({ class: "nselect-group-head", id: pickerGroupId(s.id, n) })}>${esc(g.label)}</div>`;
      return `<div${attrsHTML(pickerGroupAttrs(s.id, n))}>${head}${opts}</div>`;
    })
    .join("");
  const empty =
    s.items.length === 0 ? `<div class="nselect-empty">${esc(pickerEmptyText(s))}</div>` : "";
  const create = hasCreate(s)
    ? `<div${attrsHTML(pickerCreateAttrs(s))}>${icoSvg("plus")}<span class="nselect-label">${s.createHTML ?? esc(s.create)}</span></div>`
    : "";
  return options + empty + create;
}

export function pickerChipsHTML(s: PickerState): string {
  return s.items
    .filter((i) => i.selected)
    .map((i) => tagHTML(i.label, pickerChipOptions(i)))
    .join("");
}

export function pickerHasHead(
  s: Pick<PickerState, "search" | "multi" | "chips" | "kind">,
): boolean {
  return s.search !== undefined || !!(s.multi && s.chips) || !!s.kind;
}

export function pickerRemoveButton(
  item: Pick<PickerCurrentItem, "value" | "label">,
): IconButtonOptions {
  return {
    iconOnly: true,
    icon: "x",
    size: "sm",
    variant: "danger",
    label: tagRemoveLabel(item.label),
    attrs: { "data-remove": item.value },
  };
}

export function pickerCurrentInnerHTML(id: string, c: PickerCurrent): string {
  const head = `<div${attrsHTML({ class: "nselect-group-head", id: `${pickerCurrentId(id)}-h` })}>${esc(c.heading)}</div>`;
  const rows = c.items.length
    ? c.items
        .map((it) => {
          const hint = it.hint ? `<span class="nselect-item-hint">${esc(it.hint)}</span>` : "";
          return `<div class="nselect-current-row"><span class="nselect-label">${it.labelHTML ?? esc(it.label)}</span>${hint}${buttonHTML(pickerRemoveButton(it))}</div>`;
        })
        .join("")
    : `<div class="nselect-empty">${esc(c.empty)}</div>`;
  return head + rows;
}

export function pickerCurrentAttrs(id: string): Attrs {
  return {
    id: pickerCurrentId(id),
    class: "nselect-current",
    role: "group",
    "aria-labelledby": `${pickerCurrentId(id)}-h`,
  };
}

export function pickerKindAttrs(id: string, k: PickerKind): Attrs {
  return { id: pickerKindId(id), class: "select-sm nselect-kind", "aria-label": k.label };
}

export function pickerKindOptionsHTML(k: PickerKind): string {
  return k.options
    .map(
      (o) =>
        `<option${attrsHTML({ value: o.value, selected: o.value === k.value, disabled: o.disabled })}>${esc(o.label)}</option>`,
    )
    .join("");
}

export function pickerErrorAttrs(id: string): Attrs {
  return { id: pickerErrorId(id), class: "nselect-error", role: "alert", hidden: true };
}

export function pickerHTML(opts: PickerOptions): string {
  const own = { class: pickerClasses(opts), ...pickerAttrs(opts) };
  const current = opts.current
    ? `<div${attrsHTML(pickerCurrentAttrs(opts.id))}>${pickerCurrentInnerHTML(opts.id, opts.current)}</div>`
    : "";
  let head = "";
  if (pickerHasHead(opts)) {
    const chips =
      opts.multi && opts.chips ? `<span class="nselect-head">${pickerChipsHTML(opts)}</span>` : "";
    const input = opts.search !== undefined ? `<input${attrsHTML(pickerInputAttrs(opts))}>` : "";
    const kind = opts.kind
      ? `<select${attrsHTML(pickerKindAttrs(opts.id, opts.kind))}>${pickerKindOptionsHTML(opts.kind)}</select>`
      : "";
    head = `<div class="nselect-headrow">${chips}${input}${kind}</div>`;
  }
  const hint = opts.hint ? `<div class="nselect-hint">${esc(opts.hint)}</div>` : "";
  const list = `<div${attrsHTML(pickerListAttrs(opts))}>${pickerListHTML(opts)}</div>`;
  const error = opts.errors ? `<p${attrsHTML(pickerErrorAttrs(opts.id))}></p>` : "";
  return `<div${attrsHTML(own)}${attrsHTML(opts.attrs)}>${current}${head}${hint}${list}${error}</div>`;
}

export type PickerSource = PickerItem[] | ((query: string) => PickerItem[]);

export function filterPickerItems(items: PickerItem[], query: string): PickerItem[] {
  const q = query.trim().toLowerCase();
  if (!q) return items;
  return items.filter((i) =>
    [i.label, i.hint ?? "", i.value].some((s) => s.toLowerCase().includes(q)),
  );
}

export function pickerStep(
  disabled: (i: number) => boolean,
  count: number,
  at: number,
  delta: 1 | -1,
): number {
  if (count === 0) return -1;
  let i = at < 0 ? (delta > 0 ? -1 : count) : at;
  for (let n = 0; n < count; n++) {
    i = (i + delta + count) % count;
    if (!disabled(i)) return i;
  }
  return -1;
}

export interface PickerPick {
  kind?: string;
}

export interface BindPickerOptions {
  items: PickerSource;
  // A single picker closes before onSelect runs, so focus is back on the anchor first.
  onSelect: (item: PickerItem, pick?: PickerPick) => void;
  onRemove?: (value: string) => void;
  current?: PickerCurrent | (() => PickerCurrent);
  onCreate?: (query: string) => void;
  createLabel?: (query: string) => string | null;
  empty?: string;
}

export interface PickerHandle {
  el: HTMLElement;
  refresh(items?: PickerSource): void;
  setQuery(query: string): void;
  keydown(e: KeyboardEvent): boolean;
  setError(message: string): void;
  close(restoreFocus?: boolean): void;
  isOpen(): boolean;
}

export function bindPicker(el: HTMLElement, opts: BindPickerOptions): PickerHandle {
  return wire(el, opts, readState(el), () => undefined);
}

function readState(el: HTMLElement): PickerState {
  const list = el.querySelector<HTMLElement>(".nselect-list");
  const input = el.querySelector<HTMLInputElement>(".nselect-input");
  return {
    id: el.id,
    label: list?.getAttribute("aria-label") ?? "",
    items: [],
    multi: list?.getAttribute("aria-multiselectable") === "true",
    search: input ? (input.getAttribute("placeholder") ?? "") : undefined,
    query: input?.value ?? "",
    chips: !!el.querySelector(".nselect-head"),
  };
}

const PICKER_GAP = 8;
const PICKER_MIN_ROOM = 160;

export function clipBox(el: HTMLElement): { top: number; bottom: number; scrolls: boolean } {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const oy = getComputedStyle(p).overflowY;
    if (!/(auto|scroll|hidden|clip)/.test(oy)) continue;
    const r = p.getBoundingClientRect();
    return {
      top: Math.max(r.top, 0),
      bottom: Math.min(r.bottom, window.innerHeight),
      scrolls: oy === "auto" || oy === "scroll",
    };
  }
  return { top: 0, bottom: window.innerHeight, scrolls: false };
}

function listOf(el: HTMLElement): HTMLElement {
  const list = el.querySelector<HTMLElement>(".nselect-list");
  if (!list) throw new Error("picker: no .nselect-list inside"); // i18n-ignore a programming error
  return list;
}

function wire(
  el: HTMLElement,
  opts: BindPickerOptions,
  initial: PickerState,
  onDone: (restore: boolean) => void,
): PickerHandle {
  const ctl = new AbortController();
  const signal = ctl.signal;
  const list = listOf(el);
  const input = el.querySelector<HTMLInputElement>(".nselect-input");
  const head = el.querySelector<HTMLElement>(".nselect-head");
  const currentBox = el.querySelector<HTMLElement>(".nselect-current");
  const kindEl = el.querySelector<HTMLSelectElement>(".nselect-kind");
  const errorEl = el.querySelector<HTMLElement>(".nselect-error");
  const walks = kindEl !== null || currentBox !== null;
  const base: PickerState = { ...initial, empty: opts.empty ?? initial.empty };
  let source = opts.items;
  let query = initial.query ?? "";
  let items: PickerItem[] = [];
  let active = -1;
  let open = true;

  const createText = () =>
    query.trim() && opts.createLabel ? opts.createLabel(query.trim()) : null;
  const state = (): PickerState => ({
    ...base,
    items,
    query,
    active,
    create: createText() ?? undefined,
  });
  const count = () => items.length + (createText() ? 1 : 0);
  const off = (i: number) => i < items.length && !!items[i]?.disabled;

  function load(keepActive: boolean) {
    items = typeof source === "function" ? source(query) : filterPickerItems(source, query);
    if (keepActive && active >= 0 && active < count() && !off(active)) return;
    const chosen = base.multi || query ? -1 : items.findIndex((i) => i.selected && !i.disabled);
    active = chosen >= 0 ? chosen : pickerStep(off, count(), -1, 1);
  }

  function paint() {
    const s = state();
    list.innerHTML = pickerListHTML(s);
    if (head) head.innerHTML = pickerChipsHTML(s);
    if (currentBox && opts.current) {
      const cur = typeof opts.current === "function" ? opts.current() : opts.current;
      currentBox.innerHTML = pickerCurrentInnerHTML(base.id, cur);
    }
    const at = pickerActiveId(s);
    const owner = input ?? list;
    if (at) owner.setAttribute("aria-activedescendant", at);
    else owner.removeAttribute("aria-activedescendant");
    list.querySelector<HTMLElement>(".active")?.scrollIntoView({ block: "nearest" });
  }

  function moveTo(i: number) {
    if (i === active) return;
    active = i;
    paint();
  }

  function take(i: number) {
    if (i < 0) return;
    if (i === items.length) {
      const q = query.trim();
      if (q) opts.onCreate?.(q);
      return;
    }
    const item = items.at(i);
    if (!item || item.disabled) return;
    if (!base.multi) api.close(true);
    if (kindEl) opts.onSelect(item, { kind: kindEl.value });
    else opts.onSelect(item);
    if (base.multi && open) {
      if (input) {
        input.value = "";
        query = "";
        input.focus();
      }
      load(false);
      paint();
    }
  }

  function keydown(e: KeyboardEvent): boolean {
    if (!open) return false;
    const inField = e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement;
    let to = -2;
    if (e.key === "ArrowDown") to = pickerStep(off, count(), active, 1);
    else if (e.key === "ArrowUp") to = pickerStep(off, count(), active, -1);
    else if (e.key === "Home" && !inField) to = pickerStep(off, count(), -1, 1);
    else if (e.key === "End" && !inField) to = pickerStep(off, count(), -1, -1);
    if (to !== -2) {
      e.preventDefault();
      moveTo(to);
      return true;
    }
    if (e.key === "Enter") {
      if (active < 0) return false;
      e.preventDefault();
      take(active);
      return true;
    }
    if (e.key === "Tab") {
      if (!walks) api.close(false);
      return false;
    }
    if (e.key === "Backspace" && base.multi && head && input && !input.value) {
      const last = items.filter((i) => i.selected).pop();
      if (last && opts.onRemove) {
        e.preventDefault();
        opts.onRemove(last.value);
        return true;
      }
    }
    return false;
  }

  input?.addEventListener(
    "input",
    () => {
      query = input.value;
      load(false);
      paint();
    },
    { signal },
  );
  // Only keys aimed at the field (or the list) are handled: Enter on an X button and arrows on the
  // kind select
  // must keep their native behavior. Tab is seen from every control.
  el.addEventListener(
    "keydown",
    (e) => {
      if (e.target === (input ?? list) || e.key === "Tab") keydown(e);
    },
    { signal },
  );
  // Keep focus in the field (or a mention's textarea) when an option is clicked.
  list.addEventListener("mousedown", (e) => e.preventDefault(), { signal });
  list.addEventListener(
    "click",
    (e) => {
      const opt = (e.target as HTMLElement).closest<HTMLElement>('[role="option"]');
      if (!opt) return;
      take(opt.hasAttribute("data-create") ? items.length : Number(opt.dataset.index));
    },
    { signal },
  );
  list.addEventListener(
    "mousemove",
    (e) => {
      const opt = (e.target as HTMLElement).closest<HTMLElement>('[role="option"]');
      if (!opt || opt.getAttribute("aria-disabled") === "true") return;
      moveTo(opt.hasAttribute("data-create") ? items.length : Number(opt.dataset.index));
    },
    { signal },
  );
  currentBox?.addEventListener(
    "click",
    (e) => {
      const x = (e.target as HTMLElement).closest<HTMLElement>("[data-remove]");
      if (!x) return;
      e.stopPropagation();
      // Move focus before onRemove: the repaint destroys this X and focus would drop to the body.
      (input ?? list).focus();
      opts.onRemove?.(x.dataset.remove ?? "");
    },
    { signal },
  );
  if (walks) {
    el.addEventListener(
      "focusout",
      (e) => {
        const to = e.relatedTarget;
        if (to instanceof Node && !el.contains(to)) api.close(false);
      },
      { signal },
    );
  }
  head?.addEventListener(
    "click",
    (e) => {
      const x = (e.target as HTMLElement).closest<HTMLElement>("[data-remove]");
      if (!x) return;
      e.stopPropagation();
      opts.onRemove?.(x.dataset.remove ?? "");
      input?.focus();
    },
    { signal },
  );

  const api: PickerHandle = {
    el,
    refresh(next) {
      if (next) source = next;
      load(true);
      paint();
    },
    setQuery(q) {
      query = q;
      if (input) input.value = q;
      load(false);
      paint();
    },
    keydown,
    setError(message) {
      if (!errorEl) return;
      errorEl.textContent = message;
      errorEl.hidden = !message;
    },
    close(restore = true) {
      if (!open) return;
      open = false;
      ctl.abort();
      onDone(restore);
    },
    isOpen: () => open,
  };
  load(false);
  paint();
  return api;
}

export interface OpenPickerOptions extends BindPickerOptions, Omit<PickerLook, "up"> {
  label: string;
  multi?: boolean;
  kind?: PickerKind;
  errors?: boolean;
  search?: string;
  chips?: boolean;
  hint?: string;
  id?: string;
  mount?: HTMLElement;
  focus?: boolean;
  onClose?: () => void;
}

let seq = 0;

export function openPicker(anchor: HTMLElement, opts: OpenPickerOptions): PickerHandle {
  const place: PickerPlace = opts.place ?? (opts.mount ? "prop" : "floating");
  const mount = opts.mount ?? anchor.parentElement ?? document.body;
  const id = opts.id ?? `picker-${++seq}`;
  const shell = document.createElement("div");
  shell.innerHTML = pickerHTML({
    id,
    label: opts.label,
    items: [],
    multi: opts.multi,
    search: opts.search,
    chips: opts.chips,
    hint: opts.hint,
    empty: opts.empty,
    current: opts.current
      ? typeof opts.current === "function"
        ? opts.current()
        : opts.current
      : undefined,
    kind: opts.kind,
    errors: opts.errors,
    place,
    width: opts.width,
    start: opts.start,
    cls: opts.cls,
  });
  const el = shell.firstElementChild as HTMLElement;
  mount.appendChild(el);

  const follow = new AbortController();
  let release: (() => void) | null = null;
  const handle = wire(el, opts, readState(el), (restore) => {
    const hadFocus = el.contains(document.activeElement);
    follow.abort();
    release?.();
    el.remove();
    if (restore && (hadFocus || opts.focus === false)) anchor.focus();
    opts.onClose?.();
  });

  const fit = () => {
    if (place === "floating") {
      floatAt(el, anchor.getBoundingClientRect(), "start");
      return;
    }
    if (place !== "prop") return;
    el.classList.remove("nselect-up");
    el.style.removeProperty("--nselect-room");
    if (getComputedStyle(el).position === "static") {
      el.scrollIntoView({ block: "nearest" });
      return;
    }
    const box = clipBox(mount);
    const row = mount.getBoundingClientRect();
    const want = el.getBoundingClientRect().height;
    const below = box.bottom - row.bottom - PICKER_GAP;
    const above = row.top - box.top - PICKER_GAP;
    if (want <= below) return;
    if (want <= above) {
      el.classList.add("nselect-up");
      return;
    }
    if (box.scrolls) {
      el.scrollIntoView({ block: "nearest" });
      const left = box.bottom - mount.getBoundingClientRect().bottom - PICKER_GAP;
      if (want > left) {
        el.style.setProperty("--nselect-room", `${Math.max(PICKER_MIN_ROOM, left)}px`);
      }
      return;
    }
    el.classList.toggle("nselect-up", above > below);
    el.style.setProperty("--nselect-room", `${Math.max(PICKER_MIN_ROOM, above, below)}px`);
  };
  fit();
  window.addEventListener("resize", fit, { signal: follow.signal });
  if (place === "floating") {
    window.addEventListener("scroll", fit, { signal: follow.signal, capture: true, passive: true });
  }
  if (place !== "inline") {
    release = dismissLayer(el, anchor, (restore) => handle.close(restore));
  }
  if (opts.focus !== false) {
    (
      el.querySelector<HTMLElement>(".nselect-input") ??
      el.querySelector<HTMLElement>(".nselect-list")
    )?.focus();
  }
  return handle;
}
