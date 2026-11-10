// checkHTML() and checkGroupHTML() must match Check.astro and CheckGroup.astro; classes live in
// styles/primitives/check.css.

import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type CheckType = "checkbox" | "radio";

const TYPES = new Set<string>(["checkbox", "radio"]);

export interface CheckLook {
  card?: boolean;
  head?: boolean;
  cls?: string;
}

export function checkClasses(look: CheckLook = {}): string {
  const { card, head, cls } = look;
  if (card && head) throw new Error("check: a card cannot be a list head"); // i18n-ignore a programming error
  return ["check", card && "check-card", head && "check-head", cls].filter(Boolean).join(" ");
}

export function checkType(type: CheckType = "checkbox"): CheckType {
  if (!TYPES.has(type)) throw new Error(`check: no type "${type}"`); // i18n-ignore a programming error
  return type;
}

export interface CheckParts {
  id?: string;
  hint?: boolean;
  reason?: boolean;
  errorId?: string;
}

export interface CheckIds {
  label?: string;
  hint?: string;
  reason?: string;
  labelledBy?: string;
  describedBy?: string;
}

export function checkIds(p: CheckParts): CheckIds {
  if (!p.id) return {};
  const hint = p.hint ? `${p.id}-hint` : undefined;
  const reason = p.reason ? `${p.id}-reason` : undefined;
  const described = [hint, reason, p.errorId].filter(Boolean).join(" ");
  return {
    label: `${p.id}-label`,
    hint,
    reason,
    labelledBy: `${p.id}-label`,
    describedBy: described || undefined,
  };
}

export function checkReason(reason: string | undefined, disabled: boolean | undefined): void {
  if (reason && !disabled) throw new Error("check: a reason explains a disabled check"); // i18n-ignore a programming error
}

export interface CheckOptions extends CheckLook {
  type?: CheckType;
  name?: string;
  value?: string;
  label?: string;
  labelHTML?: string;
  hint?: string;
  hintHTML?: string;
  checked?: boolean;
  disabled?: boolean;
  reason?: string;
  trailingHTML?: string;
  icon?: string;
  id?: string;
  error?: string;
  errorId?: string;
  inputCls?: string;
  inputAttrs?: Attrs;
  attrs?: Attrs;
}

function errorHTML(o: CheckOptions): string {
  if (!o.error && !o.errorId) return "";
  const own: Attrs = { class: "check-error", id: o.errorId, role: "alert", hidden: !o.error };
  return `<span${attrsHTML(own)}>${esc(o.error)}</span>`;
}

export function checkHTML(o: CheckOptions): string {
  const type = checkType(o.type);
  checkReason(o.reason, o.disabled);
  const hasHint = o.hint !== undefined || o.hintHTML !== undefined;
  const ids = checkIds({ id: o.id, hint: hasHint, reason: !!o.reason, errorId: o.errorId });
  const input: Attrs = {
    type,
    class: ["check-input", o.inputCls].filter(Boolean).join(" "),
    id: o.id,
    name: o.name,
    value: o.value,
    checked: o.checked,
    disabled: o.disabled,
    "aria-labelledby": ids.labelledBy,
    "aria-describedby": ids.describedBy,
  };
  const glyph = o.icon ? icoSvg(o.icon, { cls: "check-ico" }) : "";
  const label = o.label !== undefined ? esc(o.label) : (o.labelHTML ?? "");
  const hintText = o.hint !== undefined ? esc(o.hint) : o.hintHTML;
  const hint = hasHint
    ? `<span${attrsHTML({ class: "check-hint", id: ids.hint })}>${hintText}</span>`
    : "";
  const reason = o.reason
    ? `<span${attrsHTML({ class: "check-reason", id: ids.reason })}>${esc(o.reason)}</span>`
    : "";
  const trailing = o.trailingHTML ? `<span class="check-trailing">${o.trailingHTML}</span>` : "";
  return (
    `<label${attrsHTML({ class: checkClasses(o) })}${attrsHTML(o.attrs)}>` +
    `<input${attrsHTML(input)}${attrsHTML(o.inputAttrs)}>${glyph}` +
    `<span class="check-body"><span${attrsHTML({ class: "check-label", id: ids.label })}>${label}</span>` +
    `${hint}${reason}${errorHTML(o)}</span>${trailing}</label>`
  );
}

export function checkEl(o: CheckOptions): HTMLLabelElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = checkHTML(o);
  return tpl.content.firstElementChild as HTMLLabelElement;
}

export type CheckLayout = "stack" | "grid" | "list";

const LAYOUTS = new Set<string>(["stack", "grid", "list"]);

export interface CheckGroupLook {
  layout?: CheckLayout;
  wide?: boolean;
  cls?: string;
}

export function checkGroupClasses(look: CheckGroupLook = {}): string {
  const { layout = "stack", wide, cls } = look;
  if (!LAYOUTS.has(layout)) throw new Error(`check: no layout "${layout}"`); // i18n-ignore a programming error
  if (wide && layout !== "grid") throw new Error("check: only a grid is wide"); // i18n-ignore a programming error
  return [
    "check-group",
    layout === "grid" && "check-group-grid",
    wide && "check-group-wide",
    layout === "list" && "check-list",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface CheckGroupOptions extends CheckGroupLook {
  type?: CheckType;
  name?: string;
  label?: string;
  labelledBy?: string;
  items?: CheckOptions[];
  itemsHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function checkGroupHTML(o: CheckGroupOptions): string {
  const type = checkType(o.type);
  const own: Attrs = {
    class: checkGroupClasses(o),
    id: o.id,
    role: type === "radio" ? "radiogroup" : "group",
    "aria-label": o.label,
    "aria-labelledby": o.labelledBy,
  };
  const body = o.items
    ? o.items
        .map((it) => checkHTML({ ...it, type: it.type ?? type, name: it.name ?? o.name }))
        .join("")
    : (o.itemsHTML ?? "");
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${body}</div>`;
}
