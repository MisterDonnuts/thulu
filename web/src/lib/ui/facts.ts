// Keep in sync with Facts.astro and styles/primitives/facts.css.

import { attrsHTML, esc, type Attrs } from "./dom";

export type FactsLayout = "columns" | "inline" | "grid" | "stacked";

export interface Fact {
  label: string;
  value?: string | number | null;
  valueHTML?: string;
  mono?: boolean;
  note?: string;
  attrs?: Attrs;
  valueAttrs?: Attrs;
}

const LAYOUTS = new Set<string>(["columns", "inline", "grid", "stacked"]);

export interface FactsLook {
  layout?: FactsLayout;
  boxed?: boolean;
  cls?: string;
}

export function factsClasses(look: FactsLook = {}): string {
  const { layout = "columns", boxed = false, cls } = look;
  if (!LAYOUTS.has(layout)) throw new Error(`facts: no layout "${layout}"`); // i18n-ignore a programming error
  return [
    "fact-list",
    layout !== "columns" && `fact-list-${layout}`,
    boxed && "fact-list-boxed",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function factRows(rows: Fact[]): Fact[] {
  return rows.filter(
    (r) => r.valueHTML !== undefined || (r.value !== null && r.value !== undefined),
  );
}

export function factValueClass(row: Pick<Fact, "mono">): string | undefined {
  return row.mono ? "fact-list-mono" : undefined;
}

export function factHTML(row: Fact): string {
  const value =
    row.valueHTML ?? esc(row.value === null || row.value === undefined ? "" : String(row.value));
  const note = row.note ? `<span class="fact-list-note">${esc(row.note)}</span>` : "";
  const dd = attrsHTML({ class: factValueClass(row), ...row.valueAttrs });
  return `<div${attrsHTML(row.attrs)}><dt>${esc(row.label)}</dt><dd${dd}>${value}${note}</dd></div>`;
}

export interface FactsOptions extends FactsLook {
  attrs?: Attrs;
}

export function factsHTML(rows: Fact[], opts: FactsOptions = {}): string {
  const attrs = { class: factsClasses(opts), ...opts.attrs };
  return `<dl${attrsHTML(attrs)}>${factRows(rows).map(factHTML).join("")}</dl>`;
}
