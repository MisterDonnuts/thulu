// SPAs answer the network before they repaint, so measured checks poll until match or timeout.

const DEFAULT_TIMEOUT_MS = 5_000;
const POLL_MS = 150;

const MEASURE = {
  visible: async (loc) => ((await loc.isVisible()) ? "visible" : "hidden"),
  hidden: async (loc) => ((await loc.isVisible()) ? "visible" : "hidden"),
  text: async (loc) => ((await loc.count()) ? (await loc.innerText()).replace(/\s+/g, " ").trim() : "(missing)"),
  value: async (loc) => ((await loc.count()) ? loc.inputValue() : "(missing)"),
  count: async (loc) => String(await loc.count()),
  url: async (_loc, page) => page.url(),
  title: async (_loc, page) => page.title(),
};

export function matches(observed, expected, mode = "equals") {
  const o = String(observed ?? "").trim();
  const e = String(expected ?? "").trim();
  if (mode === "contains") return o.includes(e);
  if (mode === "regex") {
    try { return new RegExp(e).test(o); } catch { return false; }
  }
  return o === e;
}

export function defaultMatch(check) {
  return check === "url" ? "contains" : "equals";
}

/**
 * Judge a verify step. Returns { observed, pass, measured }.
 * @param {import('playwright').Page} page
 */
export async function judge(page, step) {
  const mode = step.match || defaultMatch(step.check);
  if (!step.check) {
    return { observed: step.observed, pass: matches(step.observed, step.expected, mode), measured: false };
  }
  const loc = step.target ? page.locator(step.target).first() : null;
  const countLoc = step.target ? page.locator(step.target) : null;
  const measure = () => MEASURE[step.check](step.check === "count" ? countLoc : loc, page);
  const deadline = Date.now() + (step.timeout_ms ?? DEFAULT_TIMEOUT_MS);
  let observed = "";
  for (;;) {
    try { observed = await measure(); } catch (e) { observed = `(error: ${e.message.split("\n")[0]})`; }
    if (matches(observed, step.expected, mode) || Date.now() >= deadline) break;
    await page.waitForTimeout(POLL_MS);
  }
  return { observed, pass: matches(observed, step.expected, mode), measured: true };
}
