// mdHTML(), messageHTML() and composerHTML() must match Markdown.astro, Message.astro and
// Composer.astro.

import { t } from "../../i18n";
import { chordText, MOD_KEY } from "../shortcuts";
import { buttonHTML } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export interface MdLook {
  compact?: boolean;
  cls?: string;
}

export function mdClasses(look: MdLook = {}): string {
  return ["md", look.compact && "md-compact", look.cls].filter(Boolean).join(" ");
}

export interface MdOptions extends MdLook {
  id?: string;
  attrs?: Attrs;
}

// html is written unescaped: pass only sanitized renderer output (renderMarkdown, mdBody, chatMd),
// never raw input.
export function mdHTML(html: string, opts: MdOptions = {}): string {
  const attrs = { id: opts.id, class: mdClasses(opts), ...opts.attrs };
  return `<div${attrsHTML(attrs)}>${html}</div>`;
}

export type MessageRole = "user" | "agent" | "system";

export type MessageTone = "default" | "error";

const ROLES = new Set<string>(["user", "agent", "system"]);
const TONES = new Set<string>(["default", "error"]);

export interface MessageLook {
  role: MessageRole;
  tone?: MessageTone;
  folded?: boolean;
  cls?: string;
}

export function messageClasses(look: MessageLook): string {
  const { role, tone = "default", folded = false, cls } = look;
  if (!ROLES.has(role)) throw new Error(`message: no role "${role}"`); // i18n-ignore a programming error
  if (!TONES.has(tone)) throw new Error(`message: no tone "${tone}"`); // i18n-ignore a programming error
  return ["msg", `msg-${role}`, tone === "error" && "msg-error", folded && "msg-folded", cls]
    .filter(Boolean)
    .join(" ");
}

export function messageSpeaker(look: {
  role: MessageRole;
  tone?: MessageTone;
  author?: string;
}): string {
  if (look.tone === "error") return t("ui.message.error");
  if (look.role === "system") return t("ui.message.system");
  if (look.author) return t("ui.message.named", { name: look.author });
  return look.role === "user" ? t("ui.message.you") : t("ui.message.agent");
}

export function messageBodyClasses(markdown: boolean): string {
  return markdown ? `msg-body ${mdClasses({ compact: true })}` : "msg-body msg-body-plain";
}

export interface MessageOptions extends MessageLook {
  author?: string;
  // Written unescaped: pass only the markdown renderer's sanitized output.
  bodyHTML?: string;
  text?: string;
  lead?: string;
  fold?: string;
  afterHTML?: string;
  meta?: string;
  metaHTML?: string;
  id?: string;
  attrs?: Attrs;
}

export function messageHTML(o: MessageOptions): string {
  const folded = Boolean(o.fold);
  const attrs = {
    id: o.id,
    class: messageClasses({ role: o.role, tone: o.tone, folded, cls: o.cls }),
    ...o.attrs,
  };
  const speaker = `<span class="visually-hidden">${esc(messageSpeaker(o))} </span>`;
  const glyph = o.tone === "error" ? icoSvg("warn", { cls: "msg-ico" }) : "";
  const lead = o.lead ? `<p class="msg-lead">${esc(o.lead)}</p>` : "";
  const markdown = o.bodyHTML !== undefined;
  const content = markdown ? (o.bodyHTML ?? "") : esc(o.text ?? "");
  const body = `<div class="${messageBodyClasses(markdown)}">${content}</div>`;
  const main = folded
    ? `<details class="msg-fold"><summary>${esc(o.fold)}</summary>${body}</details>`
    : body;
  const metaInner = o.metaHTML ?? (o.meta ? esc(o.meta) : "");
  const meta = metaInner ? `<div class="msg-meta">${metaInner}</div>` : "";
  return `<div${attrsHTML(attrs)}>${speaker}${glyph}${lead}${main}${o.afterHTML ?? ""}${meta}</div>`;
}

export function messageEl(o: MessageOptions): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = messageHTML(o);
  return tpl.content.firstElementChild as HTMLElement;
}

export type ComposerVariant = "comment" | "thread" | "chat";

const VARIANTS = new Set<string>(["comment", "thread", "chat"]);

export type ComposerPart =
  "input" | "audience" | "error" | "atts" | "attach" | "file" | "cancel" | "submit";

export const COMPOSER_PART_CLASS: Record<ComposerPart, string> = {
  input: "composer-input",
  audience: "composer-audience",
  error: "composer-error",
  atts: "composer-atts",
  attach: "composer-attach",
  file: "composer-file",
  cancel: "composer-cancel",
  submit: "composer-submit",
};

export interface ComposerLook {
  variant?: ComposerVariant;
  cls?: string;
}

export function composerClasses(look: ComposerLook = {}): string {
  const { variant = "comment", cls } = look;
  if (!VARIANTS.has(variant)) throw new Error(`composer: no variant "${variant}"`); // i18n-ignore a programming error
  return ["composer", `composer-${variant}`, cls].filter(Boolean).join(" ");
}

export function composerIds(
  id: string | undefined,
  ids: Partial<Record<ComposerPart, string>> = {},
): Partial<Record<ComposerPart, string>> {
  const out: Partial<Record<ComposerPart, string>> = {};
  for (const part of Object.keys(COMPOSER_PART_CLASS) as ComposerPart[]) {
    const v = ids[part] ?? (id ? `${id}-${part}` : undefined);
    if (v) out[part] = v;
  }
  return out;
}

export function composerKeyShortcuts(variant: ComposerVariant = "comment"): string {
  // i18n-ignore key names, as aria-keyshortcuts spells them
  return variant === "chat" ? "Enter" : "Meta+Enter Control+Enter";
}

export function composerSubmits(
  e: Pick<KeyboardEvent, "key" | "shiftKey" | "altKey" | "metaKey" | "ctrlKey" | "isComposing">,
  variant: ComposerVariant = "comment",
): boolean {
  if (e.key !== "Enter" || e.isComposing) return false;
  if (variant === "chat") return !e.shiftKey && !e.altKey && !e.metaKey && !e.ctrlKey;
  return (e.metaKey || e.ctrlKey) && !e.altKey;
}

export interface ComposerAttach {
  label?: string;
  title?: string;
}

export interface ComposerText {
  variant: ComposerVariant;
  rows: number;
  submitLabel: string;
  submitTitle: string;
  attachLabel: string;
  attachTitle: string;
  cancelLabel: string;
}

export function composerText(o: {
  variant?: ComposerVariant;
  rows?: number;
  submitLabel?: string;
  submitTitle?: string;
  attach?: boolean | ComposerAttach;
  cancel?: boolean | string;
}): ComposerText {
  const variant = o.variant ?? "comment";
  const chat = variant === "chat";
  const attach = typeof o.attach === "object" ? o.attach : {};
  return {
    variant,
    rows: o.rows ?? (variant === "comment" ? 1 : 2),
    submitLabel: o.submitLabel ?? (chat ? t("ui.composer.send") : t("ui.composer.post")),
    submitTitle:
      o.submitTitle ??
      (chat
        ? t("ui.composer.send_key")
        : t("ui.composer.post_key", { keys: chordText([MOD_KEY, t("common.kbd.enter")]) })),
    attachLabel: attach.label ?? t("ui.composer.attach"),
    attachTitle: attach.title ?? t("ui.composer.attach_title"),
    cancelLabel: typeof o.cancel === "string" ? o.cancel : t("common.buttons.cancel"),
  };
}

export function composerInputA11y(o: {
  label?: string;
  labelledBy?: string;
  ids: Partial<Record<ComposerPart, string>>;
  audience: boolean;
}): Attrs {
  if (!o.label && !o.labelledBy) throw new Error("composer: needs a label or labelledBy"); // i18n-ignore a programming error
  const describedBy = [o.audience && o.ids.audience, o.ids.error].filter(Boolean).join(" ");
  return {
    "aria-label": o.labelledBy ? undefined : o.label,
    "aria-labelledby": o.labelledBy,
    "aria-describedby": describedBy || undefined,
  };
}

export interface ComposerOptions extends ComposerLook {
  id?: string;
  ids?: Partial<Record<ComposerPart, string>>;
  label?: string;
  labelledBy?: string;
  placeholder?: string;
  rows?: number;
  value?: string;
  audience?: string | true;
  attach?: boolean | ComposerAttach;
  cancel?: boolean | string;
  submitLabel?: string;
  submitTitle?: string;
  ready?: boolean;
  extraHTML?: string;
  as?: "div" | "form";
  inputCls?: string;
  attrs?: Attrs;
  inputAttrs?: Attrs;
  audienceAttrs?: Attrs;
  cancelAttrs?: Attrs;
  submitAttrs?: Attrs;
}

export function composerHTML(o: ComposerOptions): string {
  const text = composerText(o);
  const { variant } = text;
  const chat = variant === "chat";
  const as = o.as ?? (chat ? "form" : "div");
  const ids = composerIds(o.id, o.ids);
  const hasAudience = o.audience !== undefined;
  const input = `<textarea${attrsHTML({
    id: ids.input,
    class: [COMPOSER_PART_CLASS.input, o.inputCls].filter(Boolean).join(" "),
    rows: text.rows,
    placeholder: o.placeholder,
    ...composerInputA11y({ label: o.label, labelledBy: o.labelledBy, ids, audience: hasAudience }),
    ...o.inputAttrs,
  })}>${esc(o.value ?? "")}</textarea>`;

  const submitType = as === "form" ? "submit" : "button";
  const keys = composerKeyShortcuts(variant);
  const submit = chat
    ? buttonHTML({
        iconOnly: true,
        icon: "send",
        size: "lg",
        variant: "primary",
        label: text.submitLabel,
        title: text.submitTitle,
        type: submitType,
        id: ids.submit,
        cls: `${COMPOSER_PART_CLASS.submit} composer-send`,
        attrs: { "aria-keyshortcuts": keys, ...o.submitAttrs },
      })
    : buttonHTML({
        label: text.submitLabel,
        size: "sm",
        variant: o.ready ? "primary" : "default",
        title: text.submitTitle,
        type: submitType,
        id: ids.submit,
        cls: COMPOSER_PART_CLASS.submit,
        attrs: { "aria-keyshortcuts": keys, ...o.submitAttrs },
      });

  const attach = o.attach
    ? buttonHTML({
        label: text.attachLabel,
        icon: "file",
        size: "sm",
        variant: "ghost",
        title: text.attachTitle,
        id: ids.attach,
        cls: COMPOSER_PART_CLASS.attach,
      }) +
      `<input${attrsHTML({ type: "file", id: ids.file, class: COMPOSER_PART_CLASS.file, multiple: true, hidden: true })}>`
    : "";
  const cancel = o.cancel
    ? buttonHTML({
        label: text.cancelLabel,
        size: "sm",
        variant: "ghost",
        cls: COMPOSER_PART_CLASS.cancel,
        attrs: o.cancelAttrs,
      })
    : "";
  const audience = hasAudience
    ? `<span${attrsHTML({ id: ids.audience, class: COMPOSER_PART_CLASS.audience, ...o.audienceAttrs })}>${o.audience === true ? "" : esc(o.audience)}</span>`
    : "";
  const atts = o.attach
    ? `<div${attrsHTML({ id: ids.atts, class: COMPOSER_PART_CLASS.atts, hidden: true })}></div>`
    : "";
  const error = `<p${attrsHTML({ id: ids.error, class: `field-error ${COMPOSER_PART_CLASS.error}`, role: "alert", hidden: true })}></p>`;
  const extra = o.extraHTML ?? "";

  const rootAttrs = attrsHTML({
    id: o.id,
    class: composerClasses({ variant, cls: o.cls }),
    ...o.attrs,
  });
  if (chat) {
    const foot = audience || extra ? `<div class="composer-foot">${audience}${extra}</div>` : "";
    return `<${as}${rootAttrs}><div class="composer-row">${input}${submit}</div>${atts}${error}${foot}</${as}>`;
  }
  const actions = `<span class="composer-actions">${attach}${cancel}${submit}</span>`;
  return `<${as}${rootAttrs}>${input}${atts}${error}<div class="composer-foot">${audience}${extra}${actions}</div></${as}>`;
}

export function composerParts(root: ParentNode): {
  input: HTMLTextAreaElement | null;
  audience: HTMLElement | null;
  error: HTMLElement | null;
  atts: HTMLElement | null;
  attach: HTMLButtonElement | null;
  file: HTMLInputElement | null;
  cancel: HTMLButtonElement | null;
  submit: HTMLButtonElement | null;
} {
  const q = <T extends Element>(part: ComposerPart) =>
    root.querySelector<T>(`.${COMPOSER_PART_CLASS[part]}`);
  return {
    input: q<HTMLTextAreaElement>("input"),
    audience: q<HTMLElement>("audience"),
    error: q<HTMLElement>("error"),
    atts: q<HTMLElement>("atts"),
    attach: q<HTMLButtonElement>("attach"),
    file: q<HTMLInputElement>("file"),
    cancel: q<HTMLButtonElement>("cancel"),
    submit: q<HTMLButtonElement>("submit"),
  };
}

export function setComposerReady(root: HTMLElement, ready: boolean): void {
  if (root.classList.contains("composer-chat")) return;
  composerParts(root).submit?.classList.toggle("btn-primary", ready);
}

// The height goes through --composer-h because inline styles may only set custom properties.
export function growComposer(ta: HTMLTextAreaElement, maxLines = 12): void {
  ta.style.setProperty("--composer-h", "auto");
  if (!ta.scrollHeight) {
    ta.style.removeProperty("--composer-h");
    return;
  }
  const cs = getComputedStyle(ta);
  const line = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.5 || 20;
  const edge = parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth) || 0;
  const pad = (parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom) || 0) + edge;
  const h = Math.min(ta.scrollHeight + edge, line * maxLines + pad);
  ta.style.setProperty("--composer-h", `${Math.ceil(h)}px`);
}
