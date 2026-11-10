// Each *HTML() here must write the same markup as its Astro twin (FilterBar, FilterField,
// SearchInput, Chip, FilterChips .astro).
// Both read their classes from the *Classes() helpers; a variant must exist here, in the component
// and in styles/primitives/toolbar.css.

import { t } from "../../i18n";
import { dotHTML, type DotTone } from "./badge";
import { countHTML } from "./count";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { kbdHTML } from "./kbd";

export type FilterBarLook = "plain" | "boxed" | "ruled" | "inset";
export type FilterBarTag = "div" | "section";

const BAR_LOOKS = new Set<string>(["plain", "boxed", "ruled", "inset"]);
const BAR_TAGS = new Set<string>(["div", "section"]);

export interface FilterBarLookOptions {
  look?: FilterBarLook;
  cls?: string;
}

export function filterBarClasses(o: FilterBarLookOptions = {}): string {
  const { look = "plain", cls } = o;
  if (!BAR_LOOKS.has(look)) throw new Error(`filter-bar: no look "${look}"`); // i18n-ignore a programming error
  return ["filter-bar", look !== "plain" && `filter-bar-${look}`, cls].filter(Boolean).join(" ");
}

export function filterBarA11y(as: FilterBarTag = "div", label?: string): Attrs {
  if (!BAR_TAGS.has(as)) throw new Error(`filter-bar: no tag "${as}"`); // i18n-ignore a programming error
  return { role: label && as === "div" ? "group" : undefined, "aria-label": label };
}

export interface FilterBarOptions extends FilterBarLookOptions {
  as?: FilterBarTag;
  label?: string;
  start: string;
  end?: string;
  id?: string;
  attrs?: Attrs;
}

export function filterBarHTML(o: FilterBarOptions): string {
  const as = o.as ?? "div";
  const own: Attrs = { id: o.id, class: filterBarClasses(o), ...filterBarA11y(as, o.label) };
  const end = o.end ? `<div class="filter-bar-end">${o.end}</div>` : "";
  return `<${as}${attrsHTML(own)}${attrsHTML(o.attrs)}>${o.start}${end}</${as}>`;
}

export interface FilterFieldLook {
  inline?: boolean;
  grow?: boolean;
  cls?: string;
}

export function filterFieldClasses(o: FilterFieldLook = {}): string {
  return [
    "field",
    "field-filter",
    o.inline && "field-filter-inline",
    o.grow && "field-filter-grow",
    o.cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function filterFieldLabelClasses(hideLabel = false): string {
  return hideLabel ? "field-label visually-hidden" : "field-label";
}

export interface FilterFieldOptions extends FilterFieldLook {
  label: string;
  for?: string;
  hideLabel?: boolean;
  control: string;
  id?: string;
  attrs?: Attrs;
}

export function filterFieldHTML(o: FilterFieldOptions): string {
  const own: Attrs = { id: o.id, class: filterFieldClasses(o) };
  const label = `<label${attrsHTML({ class: filterFieldLabelClasses(o.hideLabel), for: o.for })}>${esc(o.label)}</label>`;
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${label}${o.control}</div>`;
}

export interface SearchLook {
  fluid?: boolean;
  cls?: string;
}

export function searchClasses(o: SearchLook = {}): string {
  return ["search", o.fluid && "search-fluid", o.cls].filter(Boolean).join(" ");
}

// Width goes through --search-w, which toolbar.css uses as the flex basis so the box still shrinks
// on a phone.
export function searchStyle(width?: number): string | undefined {
  if (width === undefined) return undefined;
  if (!Number.isFinite(width) || width <= 0) throw new Error(`search: bad width ${width}`); // i18n-ignore a programming error
  return `--search-w: ${width}px`;
}

export function searchKbd(kbd?: string | false): string | undefined {
  return kbd === false ? undefined : (kbd ?? "/");
}

export function searchPlaceholder(): string {
  return t("ui.labels.search");
}

export function searchA11y(o: { placeholder?: string; label?: string; kbd?: string | false }): {
  "aria-label": string;
  "aria-keyshortcuts": string | undefined;
} {
  const placeholder = o.placeholder ?? searchPlaceholder();
  return { "aria-label": o.label ?? placeholder, "aria-keyshortcuts": searchKbd(o.kbd) };
}

export interface SearchOptions extends SearchLook {
  id: string;
  placeholder?: string;
  label?: string;
  width?: number;
  kbd?: string | false;
  type?: "text" | "search";
  value?: string;
  attrs?: Attrs;
}

export function searchHTML(o: SearchOptions): string {
  const placeholder = o.placeholder ?? searchPlaceholder();
  const kbd = searchKbd(o.kbd);
  const wrap: Attrs = { class: searchClasses(o), style: searchStyle(o.width) };
  const input: Attrs = {
    id: o.id,
    class: "search-input",
    type: o.type === "search" ? "search" : undefined,
    placeholder,
    value: o.value,
    ...searchA11y(o),
    autocomplete: "off",
  };
  const cap = kbd ? kbdHTML(kbd, { size: "sm", inset: true, decorative: true }) : "";
  return (
    `<div${attrsHTML(wrap)}>${icoSvg("search", { cls: "search-icon" })}` +
    `<input${attrsHTML(input)}${attrsHTML(o.attrs)}>${cap}</div>`
  );
}

export type ChipSize = "sm" | "md";

const CHIP_SIZES = new Set<string>(["sm", "md"]);

export interface ChipLook {
  size?: ChipSize;
  menu?: boolean;
  active?: boolean;
  cls?: string;
}

export function chipClasses(o: ChipLook = {}): string {
  const { size = "sm", menu = false, active = false, cls } = o;
  if (!CHIP_SIZES.has(size)) throw new Error(`chip: no size "${size}"`); // i18n-ignore a programming error
  return ["chip", size === "md" && "chip-md", menu && "chip-menu", active && "is-active", cls]
    .filter(Boolean)
    .join(" ");
}

export interface ChipA11yInput {
  menu?: boolean;
  pressed?: boolean;
  controls?: string;
  expanded?: boolean;
}

export function chipA11y(o: ChipA11yInput): Attrs {
  if (o.menu && o.pressed !== undefined) throw new Error("chip: a menu chip is not a toggle"); // i18n-ignore a programming error
  if (o.menu) {
    return {
      "aria-haspopup": "menu",
      "aria-expanded": o.expanded ? "true" : "false",
      "aria-controls": o.controls,
    };
  }
  return { "aria-pressed": o.pressed === undefined ? undefined : String(o.pressed) };
}

export interface ChipOptions extends ChipLook, ChipA11yInput {
  label: string;
  labelId?: string;
  icon?: string;
  dot?: DotTone;
  count?: number | string;
  countId?: string;
  hideZero?: boolean;
  disabled?: boolean;
  title?: string;
  id?: string;
  attrs?: Attrs;
}

export function chipInnerHTML(o: ChipOptions): string {
  const icon = o.icon ? icoSvg(o.icon, { size: "sm" }) : "";
  const dot = o.dot ? dotHTML(o.dot) : "";
  const label = `<span${attrsHTML({ class: "chip-label", id: o.labelId })}>${esc(o.label)}</span>`;
  const count =
    o.count !== undefined
      ? countHTML(o.count, { cls: "chip-count", id: o.countId, hideZero: o.hideZero })
      : "";
  const chevron = o.menu ? icoSvg("chevron-down", { size: "sm", cls: "chip-chevron" }) : "";
  return icon + dot + label + count + chevron;
}

export function chipHTML(o: ChipOptions): string {
  const own: Attrs = {
    id: o.id,
    class: chipClasses(o),
    type: "button",
    ...chipA11y(o),
    title: o.title,
    disabled: o.disabled,
  };
  return `<button${attrsHTML(own)}${attrsHTML(o.attrs)}>${chipInnerHTML(o)}</button>`;
}

export interface FilterChipItem {
  value: string;
  label: string;
  count?: number | string;
  dot?: DotTone;
  icon?: string;
  title?: string;
  disabled?: boolean;
}

export interface FilterChipsOptions {
  attr?: string;
  size?: ChipSize;
  label?: string;
  hideZero?: boolean;
  id?: string;
  cls?: string;
}

const DATA_NAME = /^[a-z][a-z0-9-]*$/;

export function chipDataAttr(attr = "value"): string {
  if (!DATA_NAME.test(attr)) throw new Error(`chip: bad data attribute "${attr}"`); // i18n-ignore a programming error
  return `data-${attr}`;
}

export function chipPicked(
  picked: string | readonly string[] | null | undefined,
  value: string,
): boolean {
  if (picked === null || picked === undefined) return false;
  return typeof picked === "string" ? picked === value : picked.includes(value);
}

export function filterChipOptions(
  item: FilterChipItem,
  picked: string | readonly string[] | null | undefined,
  o: FilterChipsOptions = {},
): ChipOptions {
  return {
    label: item.label,
    count: item.count,
    dot: item.dot,
    icon: item.icon,
    title: item.title,
    disabled: item.disabled,
    size: o.size,
    hideZero: o.hideZero,
    pressed: chipPicked(picked, item.value),
    attrs: { [chipDataAttr(o.attr)]: item.value },
  };
}

export function chipGroupAttrs(o: FilterChipsOptions = {}): Attrs {
  return {
    id: o.id,
    class: ["chip-group", o.cls].filter(Boolean).join(" "),
    role: "group",
    "aria-label": o.label,
  };
}

export function filterChipsHTML(
  items: readonly FilterChipItem[],
  picked: string | readonly string[] | null | undefined,
  o: FilterChipsOptions = {},
): string {
  const chips = items.map((item) => chipHTML(filterChipOptions(item, picked, o))).join("");
  return `<div${attrsHTML(chipGroupAttrs(o))}>${chips}</div>`;
}

export function setChipsPressed(
  root: ParentNode,
  picked: string | readonly string[] | null | undefined,
  attr = "value",
): void {
  const name = chipDataAttr(attr);
  for (const chip of root.querySelectorAll<HTMLElement>(`.chip[${name}]`)) {
    chip.setAttribute("aria-pressed", String(chipPicked(picked, chip.getAttribute(name) ?? "")));
  }
}
