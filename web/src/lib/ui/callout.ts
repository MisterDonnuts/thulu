// calloutHTML() must write the same markup as Callout.astro.

import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type CalloutTone = "info" | "accent" | "warn" | "danger" | "ok" | "special";

const TONES = new Set<string>(["info", "accent", "warn", "danger", "ok", "special"]);

export interface CalloutLook {
  tone?: CalloutTone;
  band?: boolean;
  bare?: boolean;
  cls?: string;
}

export function calloutClasses(look: CalloutLook = {}): string {
  const { tone = "info", band = false, bare = false, cls } = look;
  if (!TONES.has(tone)) throw new Error(`callout: no tone "${tone}"`); // i18n-ignore a programming error
  if (band && bare) throw new Error("callout: a band or bare, not both"); // i18n-ignore a programming error
  return ["callout", `callout-${tone}`, band && "callout-band", bare && "callout-bare", cls]
    .filter(Boolean)
    .join(" ");
}

export function checkBare(o: {
  icon?: unknown;
  title?: unknown;
  detail?: unknown;
  actions?: unknown;
}): void {
  if (o.icon || o.title || o.detail || o.actions)
    throw new Error("callout: a bare callout holds its text only"); // i18n-ignore a programming error
}

export interface CalloutDetail {
  summary: string;
  text: string;
}

export interface CalloutOptions extends CalloutLook {
  icon?: string;
  title?: string;
  body?: string;
  bodyHTML?: string;
  detail?: CalloutDetail;
  actionsHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function calloutDetailHTML(d: CalloutDetail): string {
  return `<details class="callout-detail"><summary>${esc(d.summary)}</summary><pre>${esc(d.text)}</pre></details>`;
}

export function calloutHTML(o: CalloutOptions): string {
  const own: Attrs = { id: o.id, class: calloutClasses(o) };
  if (o.bare) {
    checkBare({ icon: o.icon, title: o.title, detail: o.detail, actions: o.actionsHTML });
    const text = o.body !== undefined ? esc(o.body) : (o.bodyHTML ?? "");
    return `<p${attrsHTML(own)}${attrsHTML(o.attrs)}>${text}</p>`;
  }
  const glyph = o.icon ? icoSvg(o.icon, { cls: "callout-ico" }) : "";
  const title = o.title ? `<p class="callout-title">${esc(o.title)}</p>` : "";
  const text = o.body !== undefined ? esc(o.body) : o.bodyHTML;
  const body = text ? `<div class="callout-text">${text}</div>` : "";
  const detail = o.detail ? calloutDetailHTML(o.detail) : "";
  const actions = o.actionsHTML ? `<div class="callout-actions">${o.actionsHTML}</div>` : "";
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${glyph}<div class="callout-body">${title}${body}${detail}</div>${actions}</div>`;
}

export function calloutEl(o: CalloutOptions): HTMLDivElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = calloutHTML(o);
  return tpl.content.firstElementChild as HTMLDivElement;
}
