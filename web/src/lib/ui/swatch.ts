import { attrsHTML, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type SwatchKind = "color" | "glyph";

export type SwatchRole = "radio" | "menuitemradio";

const KINDS = new Set<string>(["color", "glyph"]);
const ROLES = new Set<string>(["radio", "menuitemradio"]);

export interface SwatchItem {
  value: string;
  label: string;
  color?: string;
  icon?: string;
  attrs?: Attrs;
}

export interface SwatchPickerLook {
  kind?: SwatchKind;
  boxed?: boolean;
  cls?: string;
}

export function swatchPickerClasses(look: SwatchPickerLook = {}): string {
  const { kind = "color", boxed = false, cls } = look;
  if (!KINDS.has(kind)) throw new Error(`swatch: no kind "${kind}"`); // i18n-ignore a programming error
  return ["swatch-picker", `swatch-picker-${kind}`, boxed && "swatch-picker-boxed", cls]
    .filter(Boolean)
    .join(" ");
}

export function swatchClasses(kind: SwatchKind = "color"): string {
  if (!KINDS.has(kind)) throw new Error(`swatch: no kind "${kind}"`); // i18n-ignore a programming error
  return `swatch swatch-${kind}`;
}

export function swatchPickerAttrs(role: SwatchRole = "radio", label = ""): Attrs {
  if (!ROLES.has(role)) throw new Error(`swatch: no role "${role}"`); // i18n-ignore a programming error
  return {
    role: role === "radio" ? "radiogroup" : "group",
    "aria-label": label,
    // "" and undefined, not booleans: Astro writes a false data-* as "false".
    "data-menu-row": role === "menuitemradio" ? "" : undefined,
  };
}

export function swatchTabStop(items: SwatchItem[], selected: string | undefined): number {
  const i = items.findIndex((it) => it.value === selected);
  return i < 0 ? 0 : i;
}

export interface SwatchState {
  kind?: SwatchKind;
  role?: SwatchRole;
  checked: boolean;
  tabStop: boolean;
  keepOpen?: boolean;
}

export function swatchAttrs(item: SwatchItem, s: SwatchState): Attrs {
  const kind = s.kind ?? "color";
  const role = s.role ?? "radio";
  return {
    type: "button",
    class: swatchClasses(kind),
    role,
    "aria-checked": s.checked ? "true" : "false",
    "aria-label": item.label,
    title: item.label,
    "data-value": item.value,
    style: kind === "color" ? `--sw:${item.color ?? item.value}` : undefined,
    tabindex: role === "menuitemradio" || !s.tabStop ? "-1" : "0",
    "data-keep-open": role === "menuitemradio" && s.keepOpen ? "" : undefined,
  };
}

export interface SwatchPickerOptions extends SwatchPickerLook {
  label: string;
  items: SwatchItem[];
  selected?: string;
  role?: SwatchRole;
  keepOpen?: boolean;
  id?: string;
  attrs?: Attrs;
}

export function swatchPickerHTML(o: SwatchPickerOptions): string {
  const kind = o.kind ?? "color";
  const role = o.role ?? "radio";
  const stop = swatchTabStop(o.items, o.selected);
  const own: Attrs = {
    id: o.id,
    class: swatchPickerClasses(o),
    ...swatchPickerAttrs(role, o.label),
  };
  const swatches = o.items
    .map((item, i) => {
      const a = swatchAttrs(item, {
        kind,
        role,
        checked: item.value === o.selected,
        tabStop: i === stop,
        keepOpen: o.keepOpen,
      });
      const glyph = kind === "glyph" && item.icon ? icoSvg(item.icon) : "";
      return `<button${attrsHTML(a)}${attrsHTML(item.attrs)}>${glyph}</button>`;
    })
    .join("");
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${swatches}</div>`;
}

export function checkSwatch(pick: HTMLElement): void {
  const picker = pick.closest(".swatch-picker");
  if (!picker) return;
  const radio = picker.getAttribute("role") === "radiogroup";
  picker.querySelectorAll<HTMLElement>(".swatch").forEach((s) => {
    s.setAttribute("aria-checked", String(s === pick));
    if (radio) s.tabIndex = s === pick ? 0 : -1;
  });
}

function rowLength(items: HTMLElement[]): number {
  const top = items[0]?.offsetTop ?? 0;
  const n = items.filter((s) => s.offsetTop === top).length;
  return n >= items.length ? 1 : n;
}

export function moveSwatchFocus(e: KeyboardEvent): boolean {
  const from = (e.target as HTMLElement | null)?.closest<HTMLElement>(".swatch");
  const picker = from?.closest<HTMLElement>('.swatch-picker[role="radiogroup"]');
  if (!from || !picker) return false;
  const items = Array.from(picker.querySelectorAll<HTMLElement>(".swatch"));
  const i = items.indexOf(from);
  const n = items.length;
  let to: number;
  if (e.key === "Home") to = 0;
  else if (e.key === "End") to = n - 1;
  else if (e.key === "ArrowRight") to = (i + 1) % n;
  else if (e.key === "ArrowLeft") to = (i - 1 + n) % n;
  else if (e.key === "ArrowDown") to = (i + rowLength(items)) % n;
  else if (e.key === "ArrowUp") to = (i - rowLength(items) + n) % n;
  else return false;
  e.preventDefault();
  items.forEach((s, k) => (s.tabIndex = k === to ? 0 : -1));
  items[to].focus();
  return true;
}
