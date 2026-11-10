// Keep in sync with Field.astro, FormError.astro, InlineStatus.astro and
// styles/primitives/form.css.

import { t } from "../../i18n";
import { calloutClasses } from "./callout";
import { attrsHTML, esc, type Attrs } from "./dom";

export type FieldLayout = "stacked" | "inline";

export type FieldSize = "md" | "sm";

const LAYOUTS = new Set<string>(["stacked", "inline"]);
const SIZES = new Set<string>(["md", "sm"]);

export interface FieldLook {
  layout?: FieldLayout;
  size?: FieldSize;
  grow?: boolean;
  cls?: string;
}

export function fieldClasses(look: FieldLook = {}): string {
  const { layout = "stacked", size = "md", grow = false, cls } = look;
  if (!LAYOUTS.has(layout)) throw new Error(`field: no layout "${layout}"`); // i18n-ignore a programming error
  if (!SIZES.has(size)) throw new Error(`field: no size "${size}"`); // i18n-ignore a programming error
  return [
    "field",
    layout === "inline" && "field-inline",
    size === "sm" && "field-sm",
    grow && "field-grow",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface FieldIdsInput {
  for?: string;
  helpId?: string;
  warnId?: string;
  errorId?: string;
  help?: boolean;
  warn?: boolean;
  error?: boolean;
}

export function fieldIds(o: FieldIdsInput): {
  helpId?: string;
  warnId?: string;
  errorId?: string;
} {
  const derive = (given: string | undefined, suffix: string) =>
    given ?? (o.for ? `${o.for}-${suffix}` : undefined);
  return {
    helpId: o.help ? derive(o.helpId, "help") : undefined,
    warnId: o.warn ? derive(o.warnId, "warn") : undefined,
    errorId: o.error ? derive(o.errorId, "error") : undefined,
  };
}

export function fieldOptionalText(): string {
  return t("ui.field.optional");
}

export interface FieldControlA11y extends FieldIdsInput {
  errorText?: string;
}

export function fieldControlAttrs(o: FieldControlA11y): Attrs {
  const { helpId, errorId } = fieldIds(o);
  const ids = [helpId, o.errorText ? errorId : undefined].filter(Boolean).join(" ");
  return { "aria-describedby": ids || undefined, "aria-invalid": o.errorText ? "true" : undefined };
}

export interface FieldOptions extends FieldLook {
  label: string;
  for?: string;
  optional?: boolean;
  hint?: string;
  hintHTML?: string;
  help?: string;
  helpHTML?: string;
  helpId?: string;
  warn?: string;
  warnId?: string;
  error?: string;
  errorId?: string;
  control: string;
  id?: string;
  attrs?: Attrs;
}

function labelHTML(o: FieldOptions): string {
  const optional = o.optional
    ? ` <span class="field-optional">${esc(fieldOptionalText())}</span>`
    : "";
  const hintText = o.hint !== undefined ? esc(o.hint) : o.hintHTML;
  const hint = hintText ? ` <span class="field-hint">${hintText}</span>` : "";
  return `<label${attrsHTML({ class: "field-label", for: o.for })}>${esc(o.label)}${optional}${hint}</label>`;
}

export function fieldHTML(o: FieldOptions): string {
  const helpText = o.help !== undefined ? esc(o.help) : o.helpHTML;
  const hasError = Boolean(o.error) || o.errorId !== undefined;
  const hasWarn = Boolean(o.warn) || o.warnId !== undefined;
  const { helpId, warnId, errorId } = fieldIds({
    for: o.for,
    helpId: o.helpId,
    warnId: o.warnId,
    errorId: o.errorId,
    help: Boolean(helpText),
    warn: hasWarn,
    error: hasError,
  });
  const help = helpText
    ? `<p${attrsHTML({ class: "field-help", id: helpId })}>${helpText}</p>`
    : "";
  const warn = hasWarn
    ? `<p${attrsHTML({ class: "field-warn", id: warnId, hidden: !o.warn })}>${esc(o.warn)}</p>`
    : "";
  const error = hasError
    ? `<p${attrsHTML({ class: "field-error", id: errorId, hidden: !o.error })}>${esc(o.error)}</p>`
    : "";
  const own: Attrs = { id: o.id, class: fieldClasses(o) };
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${labelHTML(o)}${o.control}${help}${warn}${error}</div>`;
}

function controlOf(field: Element): HTMLElement | null {
  const label = field.querySelector<HTMLLabelElement>(":scope > .field-label");
  const byFor = label?.htmlFor ? document.getElementById(label.htmlFor) : null;
  return byFor ?? field.querySelector<HTMLElement>("input, select, textarea");
}

function describedBy(control: HTMLElement): string[] {
  return (control.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
}

// The error line is in aria-describedby only while shown: screen readers read a hidden line a
// control still points at.
export function setFieldError(
  target: HTMLElement,
  message: string,
  opts: { focus?: boolean } = {},
): void {
  const field = target.closest(".field");
  if (!field) return;
  const control = target.matches("input, select, textarea") ? target : controlOf(field);
  const line = field.querySelector<HTMLElement>(":scope > .field-error");
  if (line) {
    line.textContent = message;
    line.hidden = !message;
  }
  if (!control) return;
  const ids = describedBy(control).filter((id) => id !== line?.id);
  if (message) {
    control.setAttribute("aria-invalid", "true");
    if (line?.id) ids.push(line.id);
  } else {
    control.removeAttribute("aria-invalid");
  }
  if (ids.length) control.setAttribute("aria-describedby", ids.join(" "));
  else control.removeAttribute("aria-describedby");
  if (message && opts.focus) control.focus();
}

export function setFieldWarning(target: HTMLElement, message: string): void {
  const field = target.closest(".field");
  if (!field) return;
  const control = target.matches("input, select, textarea") ? target : controlOf(field);
  const line = field.querySelector<HTMLElement>(":scope > .field-warn");
  if (!line) return;
  line.textContent = message;
  line.hidden = !message;
  if (!control) return;
  const ids = describedBy(control).filter((id) => id !== line.id);
  if (message && line.id) ids.push(line.id);
  if (ids.length) control.setAttribute("aria-describedby", ids.join(" "));
  else control.removeAttribute("aria-describedby");
}

export interface FieldErrorOptions {
  id?: string;
  text?: string;
  tag?: "p" | "span";
  row?: boolean;
  cls?: string;
}

export function fieldErrorHTML(o: FieldErrorOptions = {}): string {
  const tag = o.tag ?? "p";
  const attrs: Attrs = {
    id: o.id,
    class: ["field-error", o.row && "field-error-row", o.cls].filter(Boolean).join(" "),
    role: "alert",
    hidden: !o.text,
  };
  return `<${tag}${attrsHTML(attrs)}>${esc(o.text)}</${tag}>`;
}

export interface FormSectionLook {
  ruled?: boolean;
  group?: boolean;
  cls?: string;
}

export function formSectionClasses(look: FormSectionLook = {}): string {
  const { ruled = false, group = false, cls } = look;
  return ["form-section", group && "form-section-group", ruled && "form-section-ruled", cls]
    .filter(Boolean)
    .join(" ");
}

export interface FormSectionOptions extends FormSectionLook {
  legend?: string;
  legendHTML?: string;
  bodyHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function formSectionHTML(o: FormSectionOptions): string {
  const own: Attrs = { id: o.id, class: formSectionClasses(o) };
  return `<fieldset${attrsHTML(own)}${attrsHTML(o.attrs)}><legend class="form-section-legend">${o.legend !== undefined ? esc(o.legend) : (o.legendHTML ?? "")}</legend><div class="form-section-body">${o.bodyHTML ?? ""}</div></fieldset>`;
}

export interface FormErrorOptions {
  id?: string;
  text?: string;
  cls?: string;
}

export function formErrorHTML(o: FormErrorOptions = {}): string {
  const attrs: Attrs = {
    id: o.id,
    class: formErrorClasses(o.cls),
    role: "alert",
    hidden: !o.text,
  };
  return `<p${attrsHTML(attrs)}>${esc(o.text)}</p>`;
}

// form-error is the hook clearFormErrors() and the pages find the box by.
export function formErrorClasses(cls?: string): string {
  return calloutClasses({
    tone: "danger",
    bare: true,
    cls: ["form-error", cls].filter(Boolean).join(" "),
  });
}

export function setFormError(el: HTMLElement, message: string): void {
  el.textContent = message;
  el.hidden = !message;
}

export function clearFormErrors(root: ParentNode): void {
  for (const line of root.querySelectorAll<HTMLElement>(".field > .field-error")) {
    const field = line.parentElement as HTMLElement;
    setFieldError(field, "");
  }
  for (const box of root.querySelectorAll<HTMLElement>(".form-error")) setFormError(box, "");
}

export type InlineStatusTone = "neutral" | "ok" | "danger";

const STATUS_TONES = new Set<string>(["neutral", "ok", "danger"]);

function statusTone(tone: string): string {
  if (!STATUS_TONES.has(tone)) throw new Error(`inline-status: no tone "${tone}"`); // i18n-ignore a programming error
  return tone;
}

export interface InlineStatusOptions {
  id?: string;
  tone?: InlineStatusTone;
  text?: string;
  cls?: string;
}

export function inlineStatusAttrs(o: InlineStatusOptions = {}): Attrs {
  return {
    id: o.id,
    class: ["inline-status", o.cls].filter(Boolean).join(" "),
    "data-tone": statusTone(o.tone ?? "neutral"),
    role: "status",
  };
}

// Never hidden: a live region that appears together with its text is not reliably announced.
export function inlineStatusHTML(o: InlineStatusOptions = {}): string {
  return `<span${attrsHTML(inlineStatusAttrs(o))}>${esc(o.text)}</span>`;
}

const fades = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

export function setInlineStatus(
  el: HTMLElement,
  tone: InlineStatusTone,
  text: string,
  opts: { fadeMs?: number } = {},
): void {
  clearTimeout(fades.get(el));
  fades.delete(el);
  el.dataset.tone = statusTone(tone);
  el.textContent = text;
  if (text && opts.fadeMs !== undefined) {
    fades.set(
      el,
      setTimeout(() => {
        fades.delete(el);
        el.textContent = "";
      }, opts.fadeMs),
    );
  }
}
