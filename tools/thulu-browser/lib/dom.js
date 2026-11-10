import { t } from "../i18n.js";

export const INTERACTIVE_SELECTOR = [
  'a[href]',
  'button',
  'input:not([type="hidden"])',
  'textarea',
  'select',
  '[role="button"]',
  '[role="link"]',
  '[contenteditable="true"]',
].join(", ");

export async function serializeDOM(page) {
  return page.evaluate((sel) => {
    const els = Array.from(document.querySelectorAll(sel));
    return els.map((el, i) => {
      const tag = el.tagName.toLowerCase();
      const type = el.getAttribute("type") || "";
      const text = (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 80);
      const placeholder = el.getAttribute("placeholder") || "";
      const value = el.value || "";
      const href = el.getAttribute("href") || "";
      const disabled = el.disabled || el.getAttribute("aria-disabled") === "true";
      const role = el.getAttribute("role") || "";
      const ariaLabel = el.getAttribute("aria-label") || "";
      const id = el.id || "";
      const name = el.getAttribute("name") || "";

      let desc = `[${i + 1}] <${tag}`;
      if (type) desc += ` type="${type}"`;
      if (id) desc += ` id="${id}"`;
      if (name) desc += ` name="${name}"`;
      if (role) desc += ` role="${role}"`;
      desc += ">";
      if (ariaLabel) desc += ` aria="${ariaLabel}"`;
      if (placeholder) desc += ` placeholder="${placeholder}"`;
      if (value && tag !== "select") desc += ` value="${value.slice(0, 40)}"`;
      if (href) desc += ` href="${href.slice(0, 60)}"`;
      if (text && tag !== "input" && tag !== "select") desc += ` "${text}"`;
      if (disabled) desc += " [disabled]";

      return desc;
    });
  }, INTERACTIVE_SELECTOR);
}

export async function getElementAt(page, index) {
  const elements = await page.$$(INTERACTIVE_SELECTOR);
  const el = elements[index - 1];
  if (!el) throw new Error(t("errors.no_element_at", { index, count: elements.length }));
  return el;
}

export async function captureElementInfo(page, index) {
  const elements = await page.$$(INTERACTIVE_SELECTOR);
  const el = elements[index - 1];
  if (!el) return null;
  return page.evaluate((e) => {
    const tag = e.tagName.toLowerCase();
    const id = e.id;
    const name = e.getAttribute("name");
    const cls = Array.from(e.classList).slice(0, 3).join(".");
    const text = (e.textContent || "").trim().slice(0, 50);
    const type = e.getAttribute("type");
    const placeholder = e.getAttribute("placeholder");
    const href = e.getAttribute("href");
    let selector = tag;
    if (id) selector = `#${CSS.escape(id)}`;
    else if (name) selector = `${tag}[name="${name}"]`;
    return { tag, id, name, cls, text, type, placeholder, href, selector };
  }, el);
}
