// confirmDialogHTML() must match ConfirmDialog.astro; classes live in
// styles/primitives/confirm.css.

import { t, tParts, SLOT } from "../../i18n";
import { buttonHTML } from "./button";
import { calloutHTML } from "./callout";
import { checkHTML } from "./check";
import { attrsHTML, esc } from "./dom";
import { icoSvg } from "./icon";

export interface ConfirmOption {
  label: string;
  hint?: string;
  checked?: boolean;
  name?: string;
}

export interface ConfirmLink {
  href: string;
  label: string;
}

export type ConfirmValues = Record<string, string | boolean>;

export interface ConfirmResult {
  ok: boolean;
  values: ConfirmValues;
}

interface ConfirmBase {
  title: string;
  message?: string;
  details?: string[];
  detailsMono?: boolean;
  requireText?: string;
  requireMatch?: (typed: string) => boolean;
  warning?: string;
  warningLink?: ConfirmLink;
  notes?: string[];
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  icon?: string;
  idPrefix?: string;
}

interface ConfirmRuntime {
  action?: (values: ConfirmValues) => Promise<unknown>;
}

interface ConfirmFields {
  option?: ConfirmOption;
  bodyHTML?: string;
  body?: HTMLElement;
}

export type ConfirmDialogOptions = ConfirmBase & ConfirmFields;

export type ConfirmAsk = ConfirmBase &
  ConfirmRuntime & { option?: undefined; bodyHTML?: undefined; body?: undefined };

export type ConfirmFormAsk = ConfirmBase &
  ConfirmRuntime &
  ConfirmFields &
  ({ option: ConfirmOption } | { bodyHTML: string } | { body: HTMLElement });

export interface ConfirmIds {
  dialog: string;
  title: string;
  message: string;
  form: string;
  require: string;
  error: string;
  option: string;
  ok: string;
  cancel: string;
}

export function confirmIds(prefix = "confirm"): ConfirmIds {
  return {
    dialog: `${prefix}-dialog`,
    title: `${prefix}-title`,
    message: `${prefix}-msg`,
    form: `${prefix}-form`,
    require: `${prefix}-require`,
    error: `${prefix}-error`,
    option: `${prefix}-option`,
    ok: `${prefix}-ok`,
    cancel: `${prefix}-cancel`,
  };
}

function requireLabelHTML(text: string): string {
  return tParts("ui.confirm.require", { text: SLOT })
    .map((part, i) => (i % 2 ? `<code class="code-inline">${esc(text)}</code>` : esc(part)))
    .join("");
}

export function confirmDialogAttrs(
  o: Pick<ConfirmDialogOptions, "message" | "danger" | "idPrefix">,
): Record<string, string | undefined> {
  const ids = confirmIds(o.idPrefix);
  return {
    class: "confirm-dialog",
    id: ids.dialog,
    role: "alertdialog",
    "aria-modal": "true",
    "aria-labelledby": ids.title,
    "aria-describedby": o.message ? ids.message : undefined,
    "data-tone": o.danger ? "danger" : "neutral",
    tabindex: "-1",
  };
}

function confirmWarningHTML(warning: string, link?: ConfirmLink): string {
  const more = link
    ? `<p><a href="${esc(link.href)}" data-confirm-leave>${esc(link.label)}</a></p>`
    : "";
  return calloutHTML({
    tone: "warn",
    icon: "warn",
    bodyHTML: `<p>${esc(warning)}</p>${more}`,
  });
}

export function confirmDialogHTML(o: ConfirmDialogOptions): string {
  const ids = confirmIds(o.idPrefix);
  const head =
    `<div class="confirm-dialog-head">` +
    `<span class="confirm-dialog-icon">${icoSvg(o.icon ?? "alert", { size: "lg" })}</span>` +
    `<div class="confirm-dialog-title" id="${esc(ids.title)}">${esc(o.title)}</div>` +
    `</div>`;
  const parts: string[] = [];
  if (o.message)
    parts.push(`<p class="confirm-dialog-msg" id="${esc(ids.message)}">${esc(o.message)}</p>`);
  if (o.details?.length) {
    const cls = o.detailsMono
      ? "confirm-dialog-list confirm-dialog-list-mono"
      : "confirm-dialog-list";
    parts.push(`<ul class="${cls}">${o.details.map((d) => `<li>${esc(d)}</li>`).join("")}</ul>`);
  }
  if (o.bodyHTML !== undefined || o.body)
    parts.push(`<div class="confirm-dialog-body">${o.bodyHTML ?? ""}</div>`);
  if (o.option) parts.push(confirmOptionHTML(o.option, ids.option));
  if (o.requireText !== undefined)
    parts.push(
      `<div class="confirm-dialog-field">` +
        `<label for="${esc(ids.require)}">${requireLabelHTML(o.requireText)}</label>` +
        `<input id="${esc(ids.require)}" type="text" autocomplete="off" autocapitalize="off" spellcheck="false">` +
        `</div>`,
    );
  if (o.warning) parts.push(confirmWarningHTML(o.warning, o.warningLink));
  for (const note of o.notes ?? []) parts.push(`<p class="confirm-dialog-note">${esc(note)}</p>`);
  parts.push(`<div class="confirm-dialog-error" id="${esc(ids.error)}" role="alert" hidden></div>`);
  const foot =
    `<div class="confirm-dialog-foot">` +
    buttonHTML({
      label: o.cancelLabel ?? t("common.buttons.cancel"),
      variant: "ghost",
      id: ids.cancel,
    }) +
    buttonHTML({
      label: o.confirmLabel,
      variant: o.danger ? "danger" : "primary",
      type: "submit",
      id: ids.ok,
      disabled: o.requireText !== undefined,
    }) +
    `</div>`;
  return (
    `<div${attrsHTML(confirmDialogAttrs(o))}>${head}` +
    `<form class="confirm-dialog-form" id="${esc(ids.form)}" novalidate>` +
    `<div class="confirm-dialog-content">${parts.join("")}</div>${foot}</form></div>`
  );
}

function confirmOptionHTML(o: ConfirmOption, id: string): string {
  return checkHTML({
    id,
    name: o.name ?? "option",
    label: o.label,
    hint: o.hint,
    checked: !!o.checked,
  });
}

export interface MoveDestination {
  id: string;
  label: string;
}

export function destinationOptionsHTML(destinations: MoveDestination[]): string {
  return destinations.map((d) => `<option value="${esc(d.id)}">${esc(d.label)}</option>`).join("");
}

export interface MoveTasksBody {
  count: number;
  destinations: MoveDestination[];
  label: string;
  name?: string;
  id?: string;
}

export function moveTasksBodyHTML(o: MoveTasksBody): string {
  if (!o.count || !o.destinations.length) return "";
  const id = o.id ?? "confirm-move-to";
  return (
    `<div class="confirm-dialog-field">` +
    `<label for="${esc(id)}">${esc(o.label)}</label>` +
    `<select id="${esc(id)}" name="${esc(o.name ?? "move_to")}">${destinationOptionsHTML(o.destinations)}</select>` +
    `</div>`
  );
}

export function confirmValues(form: HTMLFormElement): ConfirmValues {
  const out: ConfirmValues = {};
  for (const el of Array.from(form.elements)) {
    if (
      !(
        el instanceof HTMLInputElement ||
        el instanceof HTMLSelectElement ||
        el instanceof HTMLTextAreaElement
      )
    )
      continue;
    if (!el.name || el.disabled) continue;
    if (el instanceof HTMLInputElement && el.type === "checkbox") out[el.name] = el.checked;
    else if (el instanceof HTMLInputElement && el.type === "radio") {
      if (el.checked) out[el.name] = el.value;
    } else out[el.name] = el.value;
  }
  return out;
}

const OVERLAY_ID = "confirm-overlay";

// a[href], not [href]: an svg <use href> is not a tab stop.
// i18n-ignore a CSS selector, not a message
const FOCUSABLE = 'input, select, textarea, button, a[href], [tabindex]:not([tabindex="-1"])';

let answerOpen: ((ok: boolean) => void) | null = null;

export function isConfirmOpen(): boolean {
  return document.getElementById(OVERLAY_ID)?.getAttribute("data-open") === "true";
}

function overlayEl(): HTMLElement {
  const found = document.getElementById(OVERLAY_ID);
  if (found) return found;
  const el = document.createElement("div");
  el.className = "confirm-overlay";
  el.id = OVERLAY_ID;
  el.setAttribute("data-open", "false");
  el.setAttribute("role", "presentation");
  document.body.append(el);
  return el;
}

function errorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return t("ui.confirm.failed");
}

export function confirmDialog(o: ConfirmFormAsk): Promise<ConfirmResult>;
export function confirmDialog(o: ConfirmAsk): Promise<boolean>;
export function confirmDialog(o: ConfirmAsk | ConfirmFormAsk): Promise<boolean | ConfirmResult> {
  const wantsValues = o.option !== undefined || o.bodyHTML !== undefined || o.body !== undefined;
  return openConfirm(o).then((r) => (wantsValues ? r : r.ok));
}

function openConfirm(o: ConfirmBase & ConfirmRuntime & ConfirmFields): Promise<ConfirmResult> {
  answerOpen?.(false);
  const overlay = overlayEl();
  const ids = confirmIds(o.idPrefix);
  overlay.innerHTML = confirmDialogHTML(o);
  const dialog = overlay.firstElementChild as HTMLElement;
  const part = <T extends Element>(sel: string) => dialog.querySelector(sel) as T;
  const form = part<HTMLFormElement>(".confirm-dialog-form");
  const okBtn = part<HTMLButtonElement>(`#${ids.ok}`);
  const cancelBtn = part<HTMLButtonElement>(`#${ids.cancel}`);
  const errorEl = part<HTMLElement>(`#${ids.error}`);
  const bodyEl = dialog.querySelector<HTMLElement>(".confirm-dialog-body");
  if (o.body && bodyEl) bodyEl.append(o.body);
  const requireEl = dialog.querySelector<HTMLInputElement>(`#${ids.require}`);
  const lastFocus = document.activeElement;
  let busy = false;

  return new Promise<ConfirmResult>((resolve) => {
    const finish = (ok: boolean, values: ConfirmValues = {}) => {
      if (answerOpen !== answer) return;
      answerOpen = null;
      window.removeEventListener("keydown", onKey, true);
      overlay.removeEventListener("click", onBackdrop);
      overlay.setAttribute("data-open", "false");
      overlay.replaceChildren();
      if (lastFocus instanceof HTMLElement && lastFocus.isConnected) lastFocus.focus();
      resolve({ ok, values });
    };
    const answer = (ok: boolean) => finish(ok);
    answerOpen = answer;

    const setBusy = (on: boolean) => {
      busy = on;
      okBtn.disabled = on;
      cancelBtn.disabled = on;
      dialog.toggleAttribute("aria-busy", on);
    };

    const accept = async () => {
      if (busy || okBtn.disabled) return;
      const values = confirmValues(form);
      if (o.action) {
        errorEl.hidden = true;
        errorEl.replaceChildren();
        setBusy(true);
        try {
          await o.action(values);
        } catch (err) {
          setBusy(false);
          errorEl.innerHTML = calloutHTML({
            tone: "danger",
            icon: "warn",
            body: errorMessage(err),
          });
          errorEl.hidden = false;
          return;
        }
      }
      finish(true, values);
    };

    function onBackdrop(e: MouseEvent) {
      if (e.target === overlay && !busy) finish(false);
    }

    function onKey(e: KeyboardEvent) {
      const target = e.target as HTMLElement | null;
      const inside = !!target && dialog.contains(target);
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        if (!busy) finish(false);
        return;
      }
      if (e.key === "Tab") {
        const items = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
          (el) => !el.hasAttribute("disabled") && !el.closest("[hidden]"),
        );
        if (!items.length) return;
        const first = items[0];
        const last = items[items.length - 1];
        const at = document.activeElement;
        if (e.shiftKey && (at === first || at === dialog || !inside)) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && (at === last || !inside)) {
          e.preventDefault();
          first.focus();
        }
        return;
      }
      if (e.key === "Enter" && target === dialog) {
        e.preventDefault();
        e.stopPropagation();
        void accept();
        return;
      }
      const typing =
        inside &&
        (target instanceof HTMLInputElement ||
          target instanceof HTMLTextAreaElement ||
          target instanceof HTMLSelectElement ||
          target.isContentEditable);
      if (!typing) e.stopPropagation();
    }

    form.addEventListener("submit", (e) => {
      e.preventDefault();
      void accept();
    });
    cancelBtn.addEventListener("click", () => finish(false));
    dialog.querySelector("[data-confirm-leave]")?.addEventListener("click", () => finish(false));
    if (requireEl && o.requireText !== undefined) {
      const want = o.requireText;
      const match = o.requireMatch ?? ((typed: string) => typed.trim() === want);
      requireEl.addEventListener("input", () => {
        okBtn.disabled = !match(requireEl.value);
      });
    }
    overlay.addEventListener("click", onBackdrop);
    window.addEventListener("keydown", onKey, true);
    overlay.setAttribute("data-open", "true");

    const control = bodyEl?.querySelector<HTMLElement>("input, select, textarea");
    (requireEl ?? control ?? okBtn).focus();
  });
}
