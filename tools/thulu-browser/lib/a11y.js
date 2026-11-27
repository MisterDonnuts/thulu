// Accessibility scan with axe-core, WCAG 2.2 AA by default. axe finds a part of what WCAG asks
// (contrast, names, labels, roles, structure); a clean scan does not certify a page as accessible.
//
// axe runs in legacy mode, inside the page only: the default mode opens a blank page in the same
// context to merge frame results, and that page would land in the session's video and its page
// listeners. The cost is that cross-origin iframes are not scanned.

import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

import { AxeBuilder } from "@axe-core/playwright";

import { t } from "../i18n.js";

const require = createRequire(import.meta.url);

export const IMPACTS = ["critical", "serious", "moderate", "minor"];
export const DEFAULT_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];
export const DEFAULT_FAIL_ON = "serious";
// The evidence overlays are ours, not the app's.
const OVERLAYS = ["octopus-cinema", "octopus-annotation"];
const SETTLE_MS = 8_000;
const MAX_TARGETS = 3;

let version = null;
const sources = new Map();

export function axeVersion() {
  version ??= require("axe-core/package.json").version;
  return version;
}

// axe's own source plus its messages in the session's language, when axe has them.
function axeSource(locale) {
  const lang = String(locale || "").split("-")[0].toLowerCase();
  const key = lang && lang !== "en" ? lang : "";
  if (sources.has(key)) return sources.get(key);
  let src = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
  if (key) {
    try {
      const messages = readFileSync(require.resolve(`axe-core/locales/${key}.json`), "utf8");
      // i18n-ignore code appended to axe's source, not text
      src += `\n;axe.configure({ locale: ${messages} });`;
    } catch {}
  }
  sources.set(key, src);
  return src;
}

// A node target is a CSS selector, or a list of them when the node sits inside a shadow root or an
// iframe (one selector per boundary).
function targetText(target) {
  return (Array.isArray(target) ? target : [target]).map((x) => (Array.isArray(x) ? x.join(" >>> ") : String(x))).join(" >>> ");
}

/** A selector Playwright can point at on the top document, or null across a shadow root or frame. */
export function plainTarget(target) {
  const list = Array.isArray(target) ? target : [target];
  return list.length === 1 && typeof list[0] === "string" ? list[0] : null;
}

const rank = (impact) => {
  const i = IMPACTS.indexOf(impact);
  return i < 0 ? IMPACTS.length : i;
};

/**
 * The scan in numbers: violated rules by impact, each rule with its elements, how many checks need
 * a person, and how many rules count as failing at failOn or worse.
 * @param {import('axe-core').AxeResults} results
 */
export function summarize(results, { failOn = DEFAULT_FAIL_ON, tags = DEFAULT_TAGS } = {}) {
  const counts = Object.fromEntries(IMPACTS.map((i) => [i, 0]));
  const violations = [...(results.violations ?? [])]
    .sort((a, b) => rank(a.impact) - rank(b.impact))
    .map((v) => {
      const impact = IMPACTS.includes(v.impact) ? v.impact : "minor";
      counts[impact]++;
      return {
        id: v.id,
        impact,
        help: v.help,
        help_url: v.helpUrl,
        nodes: v.nodes.length,
        targets: v.nodes.slice(0, MAX_TARGETS).map((n) => targetText(n.target)),
        plain: v.nodes.map((n) => plainTarget(n.target)).filter(Boolean),
      };
    });
  const failing = violations.filter((v) => rank(v.impact) <= rank(failOn));
  return {
    url: results.url ?? "",
    axe: results.testEngine?.version ?? axeVersion(),
    tags,
    fail_on: failOn,
    counts,
    violations,
    failing: failing.length,
    failing_ids: failing.map((v) => v.id),
    incomplete: (results.incomplete ?? []).length,
    passes: (results.passes ?? []).length,
  };
}

/**
 * Scan the page as it is now, after its network goes quiet (at most 8 s).
 * @param {import('playwright').Page} page
 * @param {{ include?: string[], exclude?: string[], tags?: string[], disableRules?: string[],
 *           failOn?: string, locale?: string }} opts  locale: axe's messages (default: the caller's)
 * @returns {Promise<{ summary: ReturnType<typeof summarize>, results: import('axe-core').AxeResults }>}
 */
export async function scanA11y(page, { include = [], exclude = [], tags, disableRules = [], failOn,
  locale = t("meta.bcp47") } = {}) {
  const useTags = tags?.length ? tags : DEFAULT_TAGS;
  const level = IMPACTS.includes(failOn) ? failOn : DEFAULT_FAIL_ON;
  await page.waitForLoadState("networkidle", { timeout: SETTLE_MS }).catch(() => {});
  let builder = new AxeBuilder({ page, axeSource: axeSource(locale) }).setLegacyMode(true).withTags(useTags);
  for (const sel of [...OVERLAYS, ...exclude]) builder = builder.exclude(sel);
  for (const sel of include) builder = builder.include(sel);
  if (disableRules.length) builder = builder.disableRules(disableRules);
  let results;
  try {
    results = await builder.analyze();
  } catch (err) {
    throw new Error(t("errors.a11y_failed", { error: String(err?.message ?? err).split("\n")[0] }));
  }
  return { summary: summarize(results, { failOn: level, tags: useTags }), results };
}

const IMPACT_KEYS = { critical: "labels.impact_critical", serious: "labels.impact_serious",
  moderate: "labels.impact_moderate", minor: "labels.impact_minor" };

export function impactLabel(impact) {
  return t(IMPACT_KEYS[impact] ?? IMPACT_KEYS.minor);
}

/** "2 critical · 1 serious · 0 moderate · 3 minor" */
export function describeCounts(counts) {
  return IMPACTS.map((i) => t("text.a11y_count", { count: counts[i] ?? 0, impact: impactLabel(i) })).join(" · ");
}

/** What a take's verdict expects: nothing at failOn or worse. */
export function a11yExpected(failOn = DEFAULT_FAIL_ON) {
  return failOn === "minor" ? t("text.a11y_expected_any") : t("text.a11y_expected", { impact: impactLabel(failOn) });
}

/** What a take's verdict observed: "2 serious (label, color-contrast)" or "none". */
export function a11yObserved(summary) {
  if (!summary.violations.length) return t("text.a11y_observed_clean");
  if (!summary.failing) return t("text.a11y_observed_none", { count: summary.violations.length });
  const byImpact = IMPACTS.map((i) => {
    const ids = summary.violations.filter((v) => v.impact === i && summary.failing_ids.includes(v.id)).map((v) => v.id);
    return ids.length ? t("text.a11y_observed_impact", { count: ids.length, impact: impactLabel(i), rules: ids.join(", ") }) : "";
  }).filter(Boolean);
  return byImpact.join("; ");
}

/** The tool's answer: counts, then each failing rule with its elements, then the caveat. */
export function describeScan(summary) {
  const lines = [
    t("text.a11y_header", { url: summary.url, axe: summary.axe, tags: summary.tags.join(", ") }),
    t("text.a11y_counts", { counts: describeCounts(summary.counts) }),
    t("text.a11y_fail_on", { impact: impactLabel(summary.fail_on), count: summary.failing }),
  ];
  for (const v of summary.violations) {
    const failing = summary.failing_ids.includes(v.id);
    const rule = { impact: impactLabel(v.impact), id: v.id, help: v.help, count: v.nodes, targets: v.targets.join(", "),
      url: v.help_url };
    lines.push(failing ? t("text.a11y_rule", rule) : t("text.a11y_rule_below", rule));
  }
  if (summary.incomplete) lines.push(t("text.a11y_incomplete", { count: summary.incomplete }));
  lines.push(t("text.a11y_caveat"));
  return lines.join("\n");
}
