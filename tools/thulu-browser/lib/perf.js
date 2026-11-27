// Lab timing for a page the agent drives: Web Vitals from the web-vitals library (attribution
// build, injected before any page script) and, per action, how long the page took to respond.
// These are numbers from one run in this browser, not field data: compare them against main in the
// same place, not against an absolute.

import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { t } from "../i18n.js";

export const METRICS = ["ttfb", "fcp", "lcp", "cls", "inp", "response"];
export const VITALS = ["ttfb", "fcp", "lcp", "cls", "inp"];

// web.dev's good / poor boundaries (good <= first, poor > second).
export const THRESHOLDS = {
  ttfb: [800, 1_800],
  fcp: [1_800, 3_000],
  lcp: [2_500, 4_000],
  cls: [0.1, 0.25],
  inp: [200, 500],
};

// Event Timing reports only interactions of at least this many ms. An interaction that happened
// but left no entry was faster than this.
export const EVENT_THRESHOLD_MS = 16;

const SETTLE_MAX_MS = Number(process.env.PERF_SETTLE_MAX_MS || 5_000);
const NET_QUIET_MS = 500;
const DOM_QUIET_MS = 300;
const POLL_MS = 100;
const PAGE_SETTLE_MS = 8_000;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function vitalsLibrary() {
  // The IIFE build is not in the package's exports map, so it is read next to the module entry.
  const entry = fileURLToPath(import.meta.resolve("web-vitals"));
  return readFileSync(path.join(path.dirname(entry), "web-vitals.attribution.iife.js"), "utf8");
}

// Runs in the page, top frame only. Mutations inside the evidence overlays (cursor, captions,
// annotations) do not count as the app responding.
function probeBody() {
  const P = { vitals: {}, mutations: [], events: [], inputs: [] };
  Object.defineProperty(window, "__thuluPerf", { value: P, configurable: false, enumerable: false });
  const now = () => performance.timeOrigin + performance.now();
  const OVERLAY = /^OCTOPUS-(CINEMA|ANNOTATION)$/;
  const isOverlay = (n) => n && n.nodeType === 1 && OVERLAY.test(n.tagName);
  const inOverlay = (n) => {
    const el = n && (n.nodeType === 1 ? n : n.parentElement);
    return Boolean(el && el.closest && el.closest("octopus-cinema,octopus-annotation"));
  };
  const push = (list, v) => { list.push(v); if (list.length > 500) list.splice(0, 250); };
  new MutationObserver((records) => {
    for (const m of records) {
      if (inOverlay(m.target)) continue;
      if (m.type === "childList") {
        const nodes = [...m.addedNodes, ...m.removedNodes];
        if (nodes.length && nodes.every(isOverlay)) continue;
      }
      push(P.mutations, now());
      return;
    }
  }).observe(document, { childList: true, subtree: true, attributes: true, characterData: true });
  try {
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (e.interactionId) push(P.events, { at: performance.timeOrigin + e.startTime, duration: e.duration });
      }
    }).observe({ type: "event", durationThreshold: 16, buffered: true });
  } catch {}
  for (const type of ["pointerdown", "keydown"]) addEventListener(type, () => push(P.inputs, now()), true);
  P.since = (t0) => {
    const muts = P.mutations.filter((x) => x >= t0);
    const events = P.events.filter((e) => e.at >= t0);
    return {
      first: muts.length ? muts[0] : 0,
      last: muts.length ? muts[muts.length - 1] : 0,
      event: events.reduce((m, e) => Math.max(m, e.duration), 0),
      inputs: P.inputs.filter((x) => x >= t0).length,
    };
  };
  P.snapshot = () => ({ url: location.href, vitals: P.vitals, inputs: P.inputs.length });
  /* global webVitals */
  if (typeof webVitals === "object") {
    const keep = (m) => {
      const a = m.attribution || {};
      P.vitals[m.name.toLowerCase()] = {
        value: m.value,
        nav: m.navigationType,
        target: a.target || a.interactionTarget || a.largestShiftTarget || "",
      };
    };
    const opts = { reportAllChanges: true, reportSoftNavs: true };
    webVitals.onTTFB(keep, opts);
    webVitals.onFCP(keep, opts);
    webVitals.onLCP(keep, opts);
    webVitals.onCLS(keep, opts);
    webVitals.onINP(keep, { ...opts, durationThreshold: 16 });
  }
}

let script = null;
/** The init script: web-vitals plus the probe, wrapped so it runs once and only in the top frame. */
export function probeScript() {
  // i18n-ignore code injected into the page
  script ??= `(() => { if (window !== window.top || window.__thuluPerf) return;\n${vitalsLibrary()}\n(${probeBody})(); })();`;
  return script;
}

const instances = new WeakMap();
const installed = new WeakSet();

function rate(metric, value) {
  const th = THRESHOLDS[metric];
  if (!th || value === null || value === undefined) return null;
  if (value <= th[0]) return "good";
  return value <= th[1] ? "needs_improvement" : "poor";
}

/**
 * Normalize what the page reported into numbers. INP without an entry but with real input in the
 * page means every interaction was under EVENT_THRESHOLD_MS: it reads as that bound, marked.
 */
export function readVitals(snap) {
  const out = { url: snap?.url ?? "" };
  for (const m of VITALS) {
    const v = snap?.vitals?.[m];
    out[m] = typeof v?.value === "number" ? round(m, v.value) : null;
  }
  out.inp_bound = out.inp === null && (snap?.inputs ?? 0) > 0;
  if (out.inp_bound) out.inp = EVENT_THRESHOLD_MS;
  const targets = {};
  for (const m of ["lcp", "cls", "inp"]) if (snap?.vitals?.[m]?.target) targets[m] = snap.vitals[m].target;
  if (Object.keys(targets).length) out.targets = targets;
  return out;
}

function round(metric, v) {
  return metric === "cls" ? Math.round(v * 1_000) / 1_000 : Math.round(v);
}

/** The value a perf check compares, from an action's measure or a page snapshot. */
export function metricValue(metric, { vitals, action } = {}) {
  if (metric === "response") {
    const v = action?.response_ms;
    return typeof v === "number" ? { value: v } : { value: null };
  }
  const v = vitals?.[metric];
  if (typeof v !== "number") return { value: null };
  return metric === "inp" && vitals.inp_bound ? { value: v, bound: true } : { value: v };
}

function numberFormat(digits) {
  return new Intl.NumberFormat(t("meta.bcp47"), { maximumFractionDigits: digits, minimumFractionDigits: 0 });
}

/** A metric value for people: "420 ms", "1.3 s" ("1,3 s" in Spanish), "0.042" for CLS. */
export function formatMetric(metric, value, { bound = false } = {}) {
  if (value === null || value === undefined) return t("labels.no_data");
  if (metric === "cls") return numberFormat(3).format(value);
  const text = value < 1_000
    ? t("text.unit_ms", { value: numberFormat(0).format(value) })
    : t("text.unit_s", { value: numberFormat(1).format(value / 1_000) });
  return bound ? t("text.metric_under", { value: text }) : text;
}

const RATING_KEYS = { good: "labels.rating_good", needs_improvement: "labels.rating_needs_improvement",
  poor: "labels.rating_poor" };

/** "LCP 1.2 s (good)" for each vital the snapshot has. */
export function describeVitals(vitals, { ratings = true } = {}) {
  return VITALS.filter((m) => vitals?.[m] !== null && vitals?.[m] !== undefined).map((m) => {
    const value = formatMetric(m, vitals[m], { bound: m === "inp" && vitals.inp_bound });
    const r = rate(m, vitals[m]);
    return ratings && r
      ? t("text.metric_rated", { metric: m.toUpperCase(), value, rating: t(RATING_KEYS[r]) })
      : t("text.metric_plain", { metric: m.toUpperCase(), value });
  }).join(" · ");
}

export class PagePerf {
  #page;
  #ready;
  #inflight = new Map();
  #lastNet = 0;
  #requests = 0;

  /** @param {import('playwright').Page} page */
  constructor(page) {
    this.#page = page;
    const counted = (r) => !["websocket", "eventsource"].includes(r.resourceType());
    page.on("request", (r) => {
      if (!counted(r)) return;
      this.#inflight.set(r, Date.now());
      this.#requests++;
      this.#lastNet = Date.now();
    });
    const done = (r) => {
      if (this.#inflight.delete(r)) this.#lastNet = Date.now();
    };
    page.on("requestfinished", done);
    page.on("requestfailed", done);
    this.#ready = this.#install();
  }

  async #install() {
    const context = this.#page.context();
    if (!installed.has(context)) {
      installed.add(context);
      await context.addInitScript({ content: probeScript() });
    }
    // The document already loaded did not run the init script.
    await this.#page.evaluate(probeScript()).catch(() => {});
  }

  get ready() {
    return this.#ready;
  }

  async #probe(t0) {
    return this.#page.evaluate((from) => window.__thuluPerf?.since(from) ?? null, t0).catch(() => null);
  }

  /** Current vitals of the page, as numbers. */
  async snapshot() {
    await this.#ready;
    const snap = await this.#page.evaluate(() => window.__thuluPerf?.snapshot() ?? null).catch(() => null);
    return readVitals(snap ?? { url: this.#page.url() });
  }

  /** Vitals after the page has had its chance to load: network idle, at most 8 s. */
  async settledSnapshot() {
    await this.#page.waitForLoadState("networkidle", { timeout: PAGE_SETTLE_MS }).catch(() => {});
    return this.snapshot();
  }

  /**
   * Start timing an action. Call it right before the input; `end()` waits until the page has
   * settled (no new request for 500 ms and no DOM change for 300 ms, at most PERF_SETTLE_MAX_MS)
   * and returns the measure.
   */
  begin() {
    // Date.now() counts whole milliseconds and the page stamps its changes with fractions of one:
    // t0 is the first instant of a fresh millisecond, so what the page did before begin() (a small
    // page parses, loads and returns from goto within one) stamps under t0 instead of passing as the
    // action's response. The wait lasts under a millisecond.
    const before = Date.now();
    let t0 = before;
    while (t0 === before) t0 = Date.now();
    const requests0 = this.#requests;
    return { t0, end: () => this.#end(t0, requests0) };
  }

  /** Time fn as one action. */
  async measure(fn) {
    await this.#ready;
    const m = this.begin();
    const result = await fn();
    return { result, perf: await m.end() };
  }

  async #end(t0, requests0) {
    await this.#ready;
    const deadline = Date.now() + SETTLE_MAX_MS;
    let probe = null;
    let capped = false;
    for (;;) {
      probe = (await this.#probe(t0)) ?? probe;
      const now = Date.now();
      const pending = [...this.#inflight.values()].some((started) => started >= t0);
      const netQuiet = !pending && now - Math.max(t0, this.#lastNet) >= NET_QUIET_MS;
      const domQuiet = now - Math.max(t0, probe?.last || 0) >= DOM_QUIET_MS;
      if (netQuiet && domQuiet) break;
      if (now >= deadline) { capped = true; break; }
      await sleep(POLL_MS);
    }
    const requests = this.#requests - requests0;
    const changed = Boolean(probe?.first);
    const since = (at) => Math.max(0, Math.round(at - t0));
    const lastActivity = Math.max(changed ? probe.last : 0, requests ? this.#lastNet : 0, t0);
    const perf = {
      response_ms: changed ? since(probe.last) : null,
      first_ms: changed ? since(probe.first) : null,
      event_ms: probe?.event ? Math.round(probe.event) : null,
      settled_ms: since(lastActivity),
      requests,
      dead: !changed && requests === 0,
    };
    if (capped) perf.capped = true;
    perf.vitals = await this.snapshot();
    return perf;
  }
}

/** The page's timing probe, created on first use so every caller shares it. */
export function perfFor(page) {
  let p = instances.get(page);
  if (!p) {
    p = new PagePerf(page);
    instances.set(page, p);
  }
  return p;
}

/** A one-line account of an action's timing, for a tool's answer. */
export function describeAction(perf) {
  if (!perf) return "";
  if (perf.dead) return t("text.step_dead");
  return t("text.step_timing", {
    response: formatMetric("response", perf.response_ms),
    settled: formatMetric("response", perf.settled_ms),
    count: perf.requests,
  });
}

export { rate as rateMetric };
