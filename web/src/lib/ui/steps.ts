import { t } from "../../i18n";
import { safeUrl } from "../safe-url";
import { buttonHTML } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type StepState = "done" | "now" | "pending" | "blocked" | "unknown";

const STATES = new Set<string>(["done", "now", "pending", "blocked", "unknown"]);

export function stepMark(state: StepState): { icon: string; label: string } {
  if (!STATES.has(state)) throw new Error(`steps: no state "${state}"`); // i18n-ignore a programming error
  if (state === "done") return { icon: "check", label: t("ui.steps.done") };
  if (state === "blocked") return { icon: "lock", label: t("ui.steps.blocked") };
  if (state === "now") return { icon: "circle-half", label: t("ui.steps.now") };
  if (state === "unknown") return { icon: "help", label: t("ui.steps.unknown") };
  return { icon: "circle", label: t("ui.steps.pending") };
}

export interface StepListLook {
  compact?: boolean;
  inline?: boolean;
  cls?: string;
}

export function stepListClasses(look: StepListLook = {}): string {
  if (look.compact && look.inline) throw new Error("steps: compact or inline, not both"); // i18n-ignore a programming error
  return [
    "step-list",
    look.compact && "step-list-compact",
    look.inline && "step-list-inline",
    look.cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function stepItemAttrs(state: StepState): Attrs {
  return {
    class: stepItemClasses(state),
    "data-state": state,
    "aria-current": state === "now" ? "step" : undefined,
  };
}

export function stepItemClasses(state: StepState): string {
  stepMark(state);
  return `step-item step-item-${state}`;
}

export interface StepFinish {
  text: string;
  href?: string;
  command?: string;
}

export interface StepAction {
  label: string;
  href?: string;
  attrs?: Attrs;
}

export function stepActionHTML(a: StepAction): string {
  return `<p class="step-action">${buttonHTML({ label: a.label, href: a.href, variant: "link", attrs: a.attrs })}</p>`;
}

export function stepFinishHTML(f: StepFinish): string {
  const href = safeUrl(f.href);
  const sentence = href ? `<a href="${esc(href)}">${esc(f.text)}</a>` : esc(f.text);
  const command = f.command ? ` <code class="code-inline">${esc(f.command)}</code>` : "";
  return sentence + command;
}

export interface StepItem {
  title: string;
  state: StepState;
  href?: string;
  detail?: string;
  note?: string;
  finish?: StepFinish;
  action?: StepAction;
  attrs?: Attrs;
}

export function stepItemHTML(s: StepItem): string {
  const mark = stepMark(s.state);
  const href = safeUrl(s.href);
  const title = href ? `<a href="${esc(href)}">${esc(s.title)}</a>` : esc(s.title);
  const lines = [
    `<p class="step-title">${title}</p>`,
    s.action ? stepActionHTML(s.action) : "",
    s.detail ? `<p class="step-detail">${esc(s.detail)}</p>` : "",
    s.note ? `<p class="step-note">${esc(s.note)}</p>` : "",
    s.finish ? `<p class="step-finish">${stepFinishHTML(s.finish)}</p>` : "",
  ].join("");
  const own = stepItemAttrs(s.state);
  return (
    `<li${attrsHTML(own)}${attrsHTML(s.attrs)}>` +
    icoSvg(mark.icon, { cls: "step-mark", label: mark.label }) +
    `<div class="step-body">${lines}</div></li>`
  );
}

export interface StepListOptions extends StepListLook {
  id?: string;
  attrs?: Attrs;
}

export function stepListHTML(steps: StepItem[], o: StepListOptions = {}): string {
  const own: Attrs = { id: o.id, class: stepListClasses(o) };
  return `<ol${attrsHTML(own)}${attrsHTML(o.attrs)}>${steps.map(stepItemHTML).join("")}</ol>`;
}
