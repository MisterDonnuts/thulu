import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type PropListLayout = "rows" | "auto" | "sheet";

const LAYOUTS = new Set<string>(["rows", "auto", "sheet"]);

export interface PropListLook {
  layout?: PropListLayout;
  cls?: string;
}

export function propListClasses(look: PropListLook = {}): string {
  const { layout = "rows", cls } = look;
  if (!LAYOUTS.has(layout)) throw new Error(`prop: no layout "${layout}"`); // i18n-ignore a programming error
  return ["prop-list", layout !== "rows" && `prop-list-${layout}`, cls].filter(Boolean).join(" ");
}

export function setPropListLayout(el: HTMLElement, layout: PropListLayout): void {
  if (!LAYOUTS.has(layout)) throw new Error(`prop: no layout "${layout}"`); // i18n-ignore a programming error
  el.classList.add("prop-list");
  el.classList.toggle("prop-list-auto", layout === "auto");
  el.classList.toggle("prop-list-sheet", layout === "sheet");
}

export interface PropRowOptions {
  name: string;
  label: string;
  icon?: string;
  valueHTML: string;
  edit?: string;
  valueAttrs?: Attrs;
  trailingHTML?: string;
  cls?: string;
  attrs?: Attrs;
}

export function propValueClasses(editable: boolean): string {
  return editable ? "nprop-value" : "nprop-value nprop-static";
}

export function propValueA11y(edit: string | undefined): Attrs {
  if (!edit) return {};
  return { tabindex: "0", role: "button", "aria-haspopup": "true", "aria-label": edit };
}

export function propRowHTML(o: PropRowOptions): string {
  const editable = Boolean(o.edit);
  const row: Attrs = { class: ["nprop", o.cls].filter(Boolean).join(" "), "data-prop": o.name };
  const value: Attrs = { class: propValueClasses(editable), ...propValueA11y(o.edit) };
  const glyph = o.icon ? icoSvg(o.icon) : "";
  const trailing = editable ? "" : (o.trailingHTML ?? "");
  return (
    `<div${attrsHTML(row)}${attrsHTML(o.attrs)}>` +
    `<span class="nprop-label">${glyph}<span>${esc(o.label)}</span></span>` +
    `<div${attrsHTML(value)}${attrsHTML(o.valueAttrs)}>${o.valueHTML}${trailing}</div>` +
    `</div>`
  );
}

export function propEmptyHTML(text: string): string {
  return `<span class="nprop-value-empty">${esc(text)}</span>`;
}
