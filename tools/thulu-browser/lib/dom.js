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

/**
 * The page as a person sees it, for a blind tester: each interactive element (same numbering as
 * serializeDOM, so an index clicks the same element) with its role, its accessible name and its
 * state. No ids, names, classes or paths: only the host a link goes to, when it leaves this one.
 * @returns {Promise<{ n: number, role: string, name: string, value: string, states: string[], host: string }[]>}
 */
export async function accessibleElements(page) {
  return page.evaluate((sel) => {
    const IMPLICIT = { a: "link", button: "button", select: "combobox", textarea: "textbox", summary: "button" };
    const INPUT_ROLE = { button: "button", submit: "button", reset: "button", checkbox: "checkbox", radio: "radio",
      range: "slider", search: "searchbox", number: "spinbutton" };
    const clean = (s) => (s || "").replace(/\s+/g, " ").trim();
    return Array.from(document.querySelectorAll(sel)).map((el, i) => {
      const tag = el.tagName.toLowerCase();
      const attr = (n) => (el.getAttribute(n) || "").trim();
      const type = (attr("type") || "text").toLowerCase();
      const role = attr("role") || (tag === "input" ? INPUT_ROLE[type] || "textbox" : IMPLICIT[tag] || tag);
      const labelledBy = attr("aria-labelledby").split(/\s+/).filter(Boolean)
        .map((id) => clean(document.getElementById(id)?.textContent)).join(" ");
      const labelFor = el.id ? clean(document.querySelector(`label[for="${CSS.escape(el.id)}"]`)?.textContent) : "";
      const field = ["input", "textarea", "select"].includes(tag);
      const name = clean(attr("aria-label") || labelledBy || labelFor || clean(el.closest("label")?.textContent) ||
        (field ? "" : el.textContent) || attr("title") || attr("alt") ||
        (field ? attr("placeholder") : "") || (tag === "input" && INPUT_ROLE[type] === "button" ? attr("value") : "")).slice(0, 80);
      const states = [];
      if (el.disabled || attr("aria-disabled") === "true") states.push("disabled");
      if (el.checked || attr("aria-checked") === "true") states.push("checked");
      if (attr("aria-expanded") === "true") states.push("expanded");
      if (attr("aria-expanded") === "false") states.push("collapsed");
      if (el.required || attr("aria-required") === "true") states.push("required");
      if (attr("aria-invalid") === "true") states.push("invalid");
      let value = "";
      if (tag === "select") value = clean(el.selectedOptions?.[0]?.textContent);
      else if (field && !["checkbox", "radio", "button", "submit", "reset"].includes(type)) {
        value = type === "password" && el.value ? "•••" : String(el.value || "").slice(0, 40);
      }
      let host = "";
      if (tag === "a" && el.href) {
        try {
          const u = new URL(el.href);
          if (u.host !== location.host) host = u.host;
        } catch {}
      }
      return { n: i + 1, role, name, value, states, host };
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
