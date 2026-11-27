// What a page complained about while the agent drove it: console errors, uncaught exceptions,
// responses with status >= 400 and requests that never got an answer. Each entry carries the time
// it arrived; callers attribute it to the step that was running then (from that step's start to the
// next one's), so a 500 that lands late stays with the click that asked for it.

import { t } from "../i18n.js";
import { scrubber } from "./scrub.js";

export const FINDING_KINDS = ["http", "failed", "console", "pageerror"];
export const MAX_TEXT = 300;
export const PER_STEP = 50;
export const PER_RUN = 200;
const MAX_RAW = 1_000;

// Chromium repeats every 4xx/5xx as a console error; the response entry already says it.
const RESOURCE_ECHO = /^Failed to load resource:/;
// The replay's host guard aborts with blockedbyclient; aborted requests are navigations away.
const NOISE_FAILURES = /net::ERR_(ABORTED|BLOCKED_BY_CLIENT)\b/;

const instances = new WeakMap();

function clip(text) {
  const s = String(text ?? "").replace(/\s+/g, " ").trim();
  return s.length > MAX_TEXT ? `${s.slice(0, MAX_TEXT - 1)}…` : s;
}

/** URL without query or fragment: they often carry tokens. */
export function bareUrl(url) {
  try {
    const u = new URL(url);
    if (!["http:", "https:"].includes(u.protocol)) return clip(url).slice(0, 80);
    return `${u.origin}${u.pathname}`;
  } catch {
    return clip(url);
  }
}

function isFavicon(url) {
  try { return new URL(url).pathname.endsWith("/favicon.ico"); } catch { return false; }
}

/** Compile ignore patterns, refusing the ones that are not valid regular expressions. */
export function ignorePatterns(list = []) {
  return list.map((p) => {
    try {
      return new RegExp(p, "i");
    } catch (e) {
      throw new Error(t("errors.bad_ignore_pattern", { pattern: p, err: e.message }));
    }
  });
}

/**
 * Turn a raw event into a finding, or null when it is noise or ignored.
 * @param {{ kind: string, text?: string, url?: string, status?: number, method?: string, resource?: string }} raw
 * @param {{ ignore?: RegExp[], scrub?: (s: string) => string }} opts
 */
export function shapeFinding(raw, { ignore = [], scrub = (s) => s } = {}) {
  if (raw.kind === "console" && RESOURCE_ECHO.test(raw.text ?? "")) return null;
  if (raw.kind === "failed" && NOISE_FAILURES.test(raw.text ?? "")) return null;
  if (raw.url && isFavicon(raw.url) && (raw.kind === "http" || raw.kind === "failed")) return null;
  const f = { kind: raw.kind };
  if (raw.text) f.text = clip(scrub(raw.text));
  if (raw.url) f.url = scrub(bareUrl(raw.url));
  if (raw.status) f.status = raw.status;
  if (raw.method) f.method = raw.method;
  if (raw.resource) f.resource = raw.resource;
  const haystack = [f.status, f.method, f.url, f.text].filter(Boolean).join(" ");
  if (ignore.some((re) => re.test(haystack))) return null;
  return f;
}

function firstLines(err) {
  // i18n-ignore the JavaScript error's own name, as the page threw it
  const head = `${err?.name || "Error"}: ${err?.message ?? String(err)}`;
  const at = String(err?.stack ?? "").split("\n").find((l) => /^\s*at\s/.test(l));
  return at ? `${head} (${at.trim()})` : head;
}

const sameAs = (a, b) => a.kind === b.kind && a.text === b.text && a.url === b.url && a.status === b.status &&
  a.method === b.method;

/**
 * Attribute findings to steps and fold repeats. A finding belongs to the last step that started
 * before it arrived; one that arrived before the first step belongs to none and is dropped.
 * @param {{ at: number }[]} entries in arrival order
 * @param {{ step: number, start: number }[]} windows in start order
 * @returns {{ findings: object[], dropped: number }}  findings carry step and count
 */
export function groupFindings(entries, windows, { perStep = PER_STEP, perRun = PER_RUN } = {}) {
  const findings = [];
  const perStepCount = new Map();
  let dropped = 0;
  let w = -1;
  for (const e of entries) {
    while (w + 1 < windows.length && windows[w + 1].start <= e.at) w++;
    if (w < 0) continue;
    const step = windows[w].step;
    const { at: _at, ...body } = e;
    const same = findings.find((f) => f.step === step && sameAs(f, body));
    if (same) { same.count++; continue; }
    const n = perStepCount.get(step) ?? 0;
    if (n >= perStep || findings.length >= perRun) { dropped++; continue; }
    perStepCount.set(step, n + 1);
    findings.push({ step, ...body, count: 1 });
  }
  return { findings, dropped };
}

/** One line for a finding, as the agent and the report read it. */
export function describeFinding(f) {
  const times = f.count > 1 ? t("text.finding_times", { count: f.count }) : "";
  switch (f.kind) {
    case "http": return t("text.finding_http", { status: f.status, method: f.method ?? "", url: f.url ?? "" }) + times;
    case "failed": return t("text.finding_failed", { method: f.method ?? "", url: f.url ?? "", error: f.text ?? "" }) + times;
    case "console": return t("text.finding_console", { text: f.text ?? "" }) + times;
    default: return t("text.finding_pageerror", { text: f.text ?? "" }) + times;
  }
}

/** Count of findings by kind (counting repeats). */
export function countByKind(findings) {
  const out = Object.fromEntries(FINDING_KINDS.map((k) => [k, 0]));
  for (const f of findings) out[f.kind] = (out[f.kind] ?? 0) + (f.count ?? 1);
  return out;
}

export class PageFindings {
  #entries = [];
  #ignore = [];
  #secrets = new Set();

  /** @param {import('playwright').Page} page */
  constructor(page) {
    page.on("console", (m) => {
      if (m.type() !== "error") return;
      this.#add({ kind: "console", text: m.text() });
    });
    page.on("pageerror", (err) => this.#add({ kind: "pageerror", text: firstLines(err) }));
    page.on("response", (r) => {
      const status = r.status();
      if (status < 400) return;
      const req = r.request();
      this.#add({ kind: "http", status, method: req.method(), url: r.url(), resource: req.resourceType() });
    });
    page.on("requestfailed", (req) => {
      this.#add({ kind: "failed", text: req.failure()?.errorText ?? "", method: req.method(), url: req.url(),
        resource: req.resourceType() });
    });
  }

  /** Keep a value (a typed secret, the caller's key) out of every finding from now on. */
  protect(value) {
    if (typeof value === "string" && value) this.#secrets.add(value);
  }

  /** Add ignore patterns (regular expressions matched against status, method, URL and text). */
  ignore(patterns = []) {
    this.#ignore.push(...ignorePatterns(patterns));
  }

  #add(raw) {
    const f = shapeFinding(raw, { ignore: this.#ignore, scrub: scrubber(this.#secrets) });
    if (!f) return;
    this.#entries.push({ at: Date.now(), ...f });
    if (this.#entries.length > MAX_RAW) this.#entries.splice(0, this.#entries.length - MAX_RAW);
  }

  /** Raw findings that arrived at or after `since` (epoch ms). */
  since(since = 0) {
    return this.#entries.filter((e) => e.at >= since);
  }

  group(windows, opts) {
    const from = windows[0]?.start ?? Infinity;
    return groupFindings(this.since(from), windows, opts);
  }
}

/** The page's collector, created on first use so every caller shares one set of listeners. */
export function findingsFor(page) {
  let f = instances.get(page);
  if (!f) {
    f = new PageFindings(page);
    instances.set(page, f);
  }
  return f;
}
