// codeBlockHTML() must match CodeBlock.astro. The Copy button must not carry the value: a secret
// cleared from the page must not survive in an attribute.

import { t } from "../../i18n";
import { chordText, MOD_KEY } from "../shortcuts";
import { attrsHTML, esc, type Attrs } from "./dom";
import { buttonHTML } from "./button";

export type CodeTone = "neutral" | "danger";

export type CodeMaxHeight = "sm" | "lg";

const TONES = new Set<string>(["neutral", "danger"]);
const HEIGHTS = new Set<string>(["sm", "lg"]);

export interface CodeBlockLook {
  multiline?: boolean;
  wrap?: boolean;
  maxHeight?: CodeMaxHeight;
  tone?: CodeTone;
  cls?: string;
}

export function codeBlockClasses(look: CodeBlockLook = {}): string {
  const { multiline = false, maxHeight, tone = "neutral", cls } = look;
  const wrap = look.wrap ?? multiline;
  if (!TONES.has(tone)) throw new Error(`code: no tone "${tone}"`); // i18n-ignore a programming error
  if (maxHeight !== undefined && !HEIGHTS.has(maxHeight))
    throw new Error(`code: no max height "${maxHeight}"`); // i18n-ignore a programming error
  return [
    "code-block",
    multiline ? "code-block-multi" : "code-block-line",
    wrap && "code-block-wrap",
    maxHeight && `code-block-h-${maxHeight}`,
    tone !== "neutral" && `code-block-${tone}`,
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function codeTextAttrs(look: CodeBlockLook, textId?: string): Attrs {
  const wrap = look.wrap ?? look.multiline ?? false;
  const scrolls = !wrap || look.maxHeight !== undefined;
  return { id: textId, class: "code-block-text", tabindex: scrolls ? 0 : undefined };
}

export function codeCopyAttrs(label?: string): Attrs {
  return {
    "data-code-copy": "",
    "aria-label": label ? t("ui.code.copy_named", { name: label }) : undefined,
    "aria-live": "polite",
  };
}

export function codeCopyButtonHTML(label?: string): string {
  return buttonHTML({
    label: t("common.buttons.copy"),
    size: "sm",
    cls: "code-block-copy",
    attrs: codeCopyAttrs(label),
  });
}

export interface CodeBlockOptions extends CodeBlockLook {
  text?: string;
  copy?: boolean;
  label?: string;
  id?: string;
  textId?: string;
  attrs?: Attrs;
}

export function codeBlockHTML(o: CodeBlockOptions = {}): string {
  const tag = o.multiline ? "pre" : "code";
  const own: Attrs = { id: o.id, class: codeBlockClasses(o) };
  const text = `<${tag}${attrsHTML(codeTextAttrs(o, o.textId))}>${esc(o.text ?? "")}</${tag}>`;
  const copy = o.copy ? codeCopyButtonHTML(o.label) : "";
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${text}${copy}</div>`;
}

export const COPY_FEEDBACK_MS = 2000;

// The Clipboard API exists only on a secure origin; a daemon reached over plain http on a LAN needs
// the execCommand fallback.
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Denied or out of focus: try execCommand below.
    }
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.className = "code-copy-buffer";
  document.body.appendChild(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
  }
}

function selectText(node: Element): void {
  const sel = window.getSelection();
  if (!sel) return;
  const range = document.createRange();
  range.selectNodeContents(node);
  sel.removeAllRanges();
  sel.addRange(range);
}

const labels = new WeakMap<HTMLButtonElement, string>();
const timers = new WeakMap<HTMLButtonElement, ReturnType<typeof setTimeout>>();

export async function copyCodeBlock(btn: HTMLButtonElement): Promise<boolean> {
  const node = btn.closest(".code-block")?.querySelector(".code-block-text");
  if (!node) return false;
  const ok = await copyText(node.textContent);
  if (!ok) selectText(node);
  if (!labels.has(btn)) labels.set(btn, btn.textContent);
  btn.textContent = ok
    ? t("ui.code.copied")
    : t("ui.code.copy_failed", { keys: chordText([MOD_KEY, "C"]) });
  btn.dataset.state = ok ? "copied" : "failed";
  clearTimeout(timers.get(btn));
  timers.set(
    btn,
    setTimeout(() => {
      btn.textContent = labels.get(btn) ?? t("common.buttons.copy");
      delete btn.dataset.state;
    }, COPY_FEEDBACK_MS),
  );
  return ok;
}

const bound = new WeakSet<Document | HTMLElement>();

// Layout.astro binds the document once; pages without Layout (login) must call this themselves.
export function bindCodeCopy(root: Document | HTMLElement = document): void {
  if (bound.has(root)) return;
  bound.add(root);
  root.addEventListener("click", (e) => {
    const btn = (e.target as Element | null)?.closest<HTMLButtonElement>("[data-code-copy]");
    if (btn && !btn.disabled) void copyCodeBlock(btn);
  });
}
