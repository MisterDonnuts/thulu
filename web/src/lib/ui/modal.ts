// modalHTML() must emit the same markup as Modal.astro.

import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { buttonHTML, type ButtonVariant, type TextButtonOptions } from "./button";
import { icoSvg } from "./icon";

export {
  bindModal,
  closeModal,
  closeTopModal,
  escapeCloses,
  isModalOpen,
  openModal,
  topModal,
  type BindModalOpts,
} from "../modal";

export type ModalSize = "sm" | "md" | "lg" | "xl";

export type ModalVariant = "banded" | "sheet" | "viewer";

const SIZES = new Set<string>(["sm", "md", "lg", "xl"]);
const VARIANTS = new Set<string>(["banded", "sheet", "viewer"]);

export interface ModalLook {
  size?: ModalSize;
  variant?: ModalVariant;
  cls?: string;
}

export function modalClasses(look: ModalLook = {}): string {
  const { size = "sm", variant = "banded", cls } = look;
  if (!SIZES.has(size)) throw new Error(`modal: no size "${size}"`); // i18n-ignore a programming error
  if (!VARIANTS.has(variant)) throw new Error(`modal: no variant "${variant}"`); // i18n-ignore a programming error
  const parts =
    variant === "banded"
      ? ["modal", size !== "sm" && `modal-${size}`]
      : ["modal", `modal-${variant}`];
  return [...parts, cls].filter(Boolean).join(" ");
}

export function overlayClasses(variant: ModalVariant = "banded"): string {
  return variant === "sheet" ? "overlay overlay-sheet" : "overlay";
}

export function modalIds(id: string): { title: string; lede: string } {
  return { title: `${id}-title`, lede: `${id}-lede` };
}

export function modalCancelButton(label?: string): TextButtonOptions {
  return {
    label: label ?? t("common.buttons.cancel"),
    kbd: t("common.kbd.esc"),
    attrs: { "data-modal-close": "" },
  };
}

export interface ModalAction {
  label: string;
  variant?: Extract<ButtonVariant, "primary" | "danger">;
  id?: string;
  icon?: string;
  kbd?: string;
  type?: "button" | "submit";
  form?: string;
  disabled?: boolean;
  attrs?: Attrs;
}

export function modalPrimaryButton(a: ModalAction): TextButtonOptions {
  return {
    label: a.label,
    variant: a.variant ?? "primary",
    id: a.id,
    icon: a.icon,
    kbd: a.kbd,
    type: a.type,
    disabled: a.disabled,
    attrs: { form: a.form, ...a.attrs },
  };
}

export interface ModalOptions {
  id: string;
  title: string;
  icon?: string;
  size?: ModalSize;
  variant?: ModalVariant;
  closable?: boolean;
  lede?: string;
  cls?: string;
  bodyClass?: string;
  attrs?: Attrs;
  headHTML?: string;
  bodyHTML?: string;
  footHTML?: string;
  cancel?: boolean | string;
  primary?: ModalAction;
  actionsHTML?: string;
  actionsStartHTML?: string;
}

function footHTML(o: ModalOptions): string {
  if (o.footHTML) return o.footHTML;
  const start = o.actionsStartHTML
    ? `<div class="modal-foot-start">${o.actionsStartHTML}</div>`
    : "";
  const cancel = o.cancel
    ? buttonHTML(modalCancelButton(typeof o.cancel === "string" ? o.cancel : undefined))
    : "";
  const primary = o.primary ? buttonHTML(modalPrimaryButton(o.primary)) : "";
  return start + (o.actionsHTML ?? "") + cancel + primary;
}

export function modalHTML(o: ModalOptions): string {
  const ids = modalIds(o.id);
  const variant = o.variant ?? "banded";
  const overlay: Attrs = { id: o.id, class: overlayClasses(variant), role: "presentation" };
  const box: Attrs = {
    class: modalClasses({ size: o.size, variant, cls: o.cls }),
    role: "dialog",
    "aria-modal": "true",
    "aria-labelledby": ids.title,
    "aria-describedby": o.lede ? ids.lede : undefined,
    tabindex: "-1",
  };
  const glyph = o.icon ? icoSvg(o.icon, { size: "lg" }) : "";
  const close = o.closable
    ? buttonHTML({
        label: t("common.buttons.close"),
        icon: "x",
        iconOnly: true,
        attrs: { "data-modal-close": "" },
      })
    : "";
  const head = `<div class="modal-head">${glyph}<h3 id="${esc(ids.title)}">${esc(o.title)}</h3>${o.headHTML ?? ""}${close}</div>`;
  const lede = o.lede ? `<p class="modal-lede" id="${esc(ids.lede)}">${esc(o.lede)}</p>` : "";
  const bodyCls = ["modal-body", o.bodyClass].filter(Boolean).join(" ");
  const body = `<div class="${esc(bodyCls)}">${lede}${o.bodyHTML ?? ""}</div>`;
  const footInner = footHTML(o);
  const foot = footInner ? `<div class="modal-foot">${footInner}</div>` : "";
  return `<div${attrsHTML(overlay)}${attrsHTML(o.attrs)}><div${attrsHTML(box)}>${head}${body}${foot}</div></div>`;
}

export function modalEl(o: ModalOptions): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = modalHTML(o);
  const el = tpl.content.firstElementChild as HTMLElement;
  document.body.appendChild(el);
  return el;
}
