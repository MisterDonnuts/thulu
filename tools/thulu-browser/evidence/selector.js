import { t } from "../i18n.js";
import { getElementAt } from "../lib/dom.js";

function describe(el) {
  const IMPLICIT = { a: "link", button: "button", select: "combobox", textarea: "textbox", h1: "heading",
    h2: "heading", h3: "heading", h4: "heading", h5: "heading", h6: "heading", img: "img", summary: "button" };
  const INPUT_ROLE = { button: "button", submit: "button", reset: "button", checkbox: "checkbox", radio: "radio",
    range: "slider", search: "searchbox", email: "textbox", tel: "textbox", text: "textbox", url: "textbox",
    number: "spinbutton", password: "textbox" };
  const tag = el.tagName.toLowerCase();
  const attr = (n) => (el.getAttribute(n) || "").trim();
  const clean = (s) => (s || "").replace(/\s+/g, " ").trim();

  let role = attr("role");
  if (!role && tag === "input") role = INPUT_ROLE[(attr("type") || "text").toLowerCase()] || "";
  if (!role && tag === "a" && !el.hasAttribute("href")) role = "";
  else if (!role) role = IMPLICIT[tag] || "";

  const labelledBy = attr("aria-labelledby").split(/\s+/).filter(Boolean)
    .map((id) => clean(document.getElementById(id)?.textContent)).join(" ");
  const labelFor = el.id ? clean(document.querySelector(`label[for="${CSS.escape(el.id)}"]`)?.textContent) : "";
  const wrapping = clean(el.closest("label")?.textContent);
  const name = clean(attr("aria-label") || labelledBy || labelFor || wrapping ||
    (["input", "textarea", "select"].includes(tag) ? "" : el.textContent) ||
    attr("title") || attr("alt") || attr("placeholder") || (tag === "input" ? attr("value") : ""));

  const path = [];
  const unique = () => {
    const found = document.querySelectorAll(path.join(" > "));
    return found.length === 1 && found[0] === el;
  };
  for (let n = el; n && n.nodeType === 1 && n !== document.documentElement; n = n.parentElement) {
    if (n !== el && n.id) { path.unshift(`#${CSS.escape(n.id)}`); break; }
    const t = n.tagName.toLowerCase();
    const same = n.parentElement ? [...n.parentElement.children].filter((c) => c.tagName === n.tagName) : [];
    let seg = same.length > 1 ? `${t}:nth-of-type(${same.indexOf(n) + 1})` : t;
    if (n === el && t === "input") seg += `[type="${(attr("type") || "text").toLowerCase()}"]`;
    path.unshift(seg);
    if (unique()) break;
  }

  return {
    tag, role, name: name.slice(0, 120), id: el.id || "",
    testid: attr("data-testid") || attr("data-test-id") || attr("data-test") || attr("data-qa") || attr("data-cy"),
    testidAttr: ["data-testid", "data-test-id", "data-test", "data-qa", "data-cy"].find((a) => el.hasAttribute(a)) || "",
    nameAttr: attr("name"), placeholder: attr("placeholder"), ariaLabel: attr("aria-label"),
    text: clean(el.textContent).slice(0, 80), cssPath: path.join(" > "),
  };
}

const q = (s) => JSON.stringify(s);

const GENERATED_ID = /(^|[-_:])(\d{2,}|r\d+|[a-f0-9]{8,})([-_:]|$)|^:|^(ember|mui|react|headlessui)/i;

export function candidates(d) {
  const out = [];
  if (d.testid) out.push(`[${d.testidAttr}=${q(d.testid)}]`);
  if (d.role && d.name) out.push(`role=${d.role}[name=${q(d.name)}]`);
  if (d.ariaLabel) out.push(`${d.tag}[aria-label=${q(d.ariaLabel)}]`);
  if (d.nameAttr) out.push(`${d.tag}[name=${q(d.nameAttr)}]`);
  if (d.placeholder) out.push(`${d.tag}[placeholder=${q(d.placeholder)}]`);
  if (d.id && !GENERATED_ID.test(d.id)) out.push(`#${cssEscape(d.id)}`);
  if (d.text && d.text.length <= 60 && !["input", "textarea", "select"].includes(d.tag)) out.push(`text=${q(d.text)}`);
  if (d.cssPath) out.push(d.cssPath);
  return out;
}

function cssEscape(s) {
  return s.replace(/([^a-zA-Z0-9_-])/g, "\\$1").replace(/^(\d)/, "\\3$1 ");
}

/**
 * The most stable selector that resolves to exactly this element.
 * @param {import('playwright').Page} page
 * @param {import('playwright').ElementHandle} handle
 */
export async function stableSelector(page, handle) {
  const d = await handle.evaluate(describe);
  for (const sel of candidates(d)) {
    try {
      const loc = page.locator(sel);
      if ((await loc.count()) !== 1) continue;
      const same = await loc.evaluate((node, h) => node === h, handle);
      if (same) return sel;
    } catch {
      // A candidate the selector engine rejects is simply not a candidate.
    }
  }
  throw new Error(t("errors.no_unique_selector", { tag: d.tag }));
}

/**
 * What the agent pointed at, as a selector: an index from browser_get_state
 * becomes a stable selector; a selector is taken as given.
 * @param {{ index?: number, selector?: string, target?: string }} ref
 */
export async function pointAt(page, ref) {
  if (ref.index !== undefined && ref.index !== null) return stableSelector(page, await getElementAt(page, ref.index));
  const sel = ref.selector || ref.target;
  if (!sel) throw new Error(t("errors.index_or_selector"));
  return sel;
}
