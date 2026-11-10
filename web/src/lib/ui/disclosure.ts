// Keep in sync with Disclosure.astro and styles/primitives/disclosure.css.

import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type DisclosureVariant = "help" | "code" | "fields" | "row";

const VARIANTS = new Set<string>(["help", "code", "fields", "row"]);

export const DISCLOSURE_CARET = "chevron-right";

export interface DisclosureLook {
  variant?: DisclosureVariant;
  ruled?: boolean;
  cls?: string;
}

export function disclosureClasses(look: DisclosureLook = {}): string {
  const { variant = "help", ruled = false, cls } = look;
  if (!VARIANTS.has(variant)) throw new Error(`disclosure: no variant "${variant}"`); // i18n-ignore a programming error
  return ["disclosure", `disclosure-${variant}`, ruled && "disclosure-ruled", cls]
    .filter(Boolean)
    .join(" ");
}

export interface DisclosureOptions extends DisclosureLook {
  summary?: string;
  summaryHTML?: string;
  body?: string;
  bodyHTML?: string;
  open?: boolean;
  id?: string;
  bodyId?: string;
  attrs?: Attrs;
}

export function disclosureHTML(o: DisclosureOptions): string {
  const own: Attrs = { id: o.id, class: disclosureClasses(o), open: o.open };
  const caret = icoSvg(DISCLOSURE_CARET, { cls: "disclosure-caret" });
  const head = o.summary !== undefined ? esc(o.summary) : (o.summaryHTML ?? "");
  const summary = `<summary class="disclosure-summary">${caret}<span>${head}</span></summary>`;
  const inner = o.body !== undefined ? `<p>${esc(o.body)}</p>` : (o.bodyHTML ?? "");
  const body = `<div${attrsHTML({ id: o.bodyId, class: "disclosure-body" })}>${inner}</div>`;
  return `<details${attrsHTML(own)}${attrsHTML(o.attrs)}>${summary}${body}</details>`;
}
