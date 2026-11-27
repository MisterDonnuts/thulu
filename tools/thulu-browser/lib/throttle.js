// Network and CPU throttling over CDP (Chromium only). The network profiles are Chrome DevTools'
// presets, which throttle per request like Network.emulateNetworkConditions does: latency in ms,
// throughput in bytes/s. Lighthouse's "Slow 4G" quotes a 150 ms RTT for its simulated mode; with
// request-level throttling it applies 150 × 3.75 = 562.5 ms, the same as the DevTools preset here.

import { t } from "../i18n.js";

export const NETWORK_PROFILES = {
  none: { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 },
  "fast-4g": { offline: false, latency: 60 * 2.75, downloadThroughput: (9_000_000 / 8) * 0.9,
    uploadThroughput: (1_500_000 / 8) * 0.9 },
  "slow-4g": { offline: false, latency: 150 * 3.75, downloadThroughput: (1_600_000 / 8) * 0.9,
    uploadThroughput: (750_000 / 8) * 0.9 },
  "3g": { offline: false, latency: 400 * 5, downloadThroughput: (500_000 / 8) * 0.8,
    uploadThroughput: (500_000 / 8) * 0.8 },
  offline: { offline: true, latency: 0, downloadThroughput: 0, uploadThroughput: 0 },
};
export const NETWORKS = Object.keys(NETWORK_PROFILES);
export const COLOR_SCHEMES = ["light", "dark"];
export const CPU_MAX = 20;

const cdpOf = new WeakMap();

/**
 * Normalize the emulation a session or a take asks for. Unknown values throw; absent or neutral
 * values drop out, so a profile with nothing set is {}.
 * @param {{ network?: string, cpu_throttle?: number, color_scheme?: string }} input
 * @returns {{ network?: string, cpu_throttle?: number, color_scheme?: string }}
 */
export function emulation({ network, cpu_throttle, color_scheme } = {}) {
  const out = {};
  if (network !== undefined && network !== null && network !== "") {
    if (!Object.hasOwn(NETWORK_PROFILES, network)) throw new Error(t("errors.bad_network", { value: network, allowed: NETWORKS.join(", ") }));
    if (network !== "none") out.network = network;
  }
  if (cpu_throttle !== undefined && cpu_throttle !== null && cpu_throttle !== "") {
    const rate = Number(cpu_throttle);
    if (!Number.isFinite(rate) || rate < 1 || rate > CPU_MAX) throw new Error(t("errors.bad_cpu_throttle", { value: cpu_throttle, max: CPU_MAX }));
    if (rate !== 1) out.cpu_throttle = rate;
  }
  if (color_scheme !== undefined && color_scheme !== null && color_scheme !== "") {
    if (!COLOR_SCHEMES.includes(color_scheme)) throw new Error(t("errors.bad_color_scheme", { value: color_scheme }));
    out.color_scheme = color_scheme;
  }
  return out;
}

const SCHEME_KEYS = { light: "labels.scheme_light", dark: "labels.scheme_dark" };

/**
 * The profile in words, for a tool's answer and a report's context line: "slow-4g · CPU 4x ·
 * dark". With no throttling it says so; the color scheme appears only when one was set.
 */
export function describeProfile(profile = {}, { viewport } = {}) {
  const parts = [];
  if (viewport) parts.push(`${viewport.width}×${viewport.height}`);
  if (profile.network) parts.push(profile.network);
  if (profile.cpu_throttle) parts.push(t("text.profile_cpu", { rate: profile.cpu_throttle }));
  if (!throttled(profile)) parts.push(t("labels.no_throttling"));
  if (profile.color_scheme) parts.push(t(SCHEME_KEYS[profile.color_scheme]));
  return parts.join(" · ");
}

/**
 * A take with the emulation an override asks for: a value replaces the take's, "none" (network)
 * or 1 (CPU) clears it. Fields the override leaves out keep the take's value.
 */
export function withEmulation(take, override = {}) {
  const asked = Object.fromEntries(["network", "cpu_throttle", "color_scheme"]
    .filter((k) => override[k] !== undefined && override[k] !== null && override[k] !== "")
    .map((k) => [k, override[k]]));
  if (!Object.keys(asked).length) return take;
  const next = emulation(asked);
  const out = { ...take };
  for (const k of Object.keys(asked)) {
    if (next[k] === undefined) delete out[k];
    else out[k] = next[k];
  }
  return out;
}

/** The emulation fields of a take or session meta, normalized. */
export function profileOf(source = {}) {
  return emulation({ network: source.network, cpu_throttle: source.cpu_throttle, color_scheme: source.color_scheme });
}

/** True when the profile slows the page down (color scheme alone does not). */
export function throttled(profile = {}) {
  return Boolean(profile.network || profile.cpu_throttle);
}

/**
 * Apply network and CPU throttling to a page. A page without throttling gets nothing (no CDP
 * session). Calling it again replaces the page's previous conditions.
 * @param {import('playwright').Page} page
 * @param {{ network?: string, cpu_throttle?: number }} profile
 */
export async function applyThrottle(page, profile = {}) {
  if (!throttled(profile) && !cdpOf.has(page)) return null;
  let cdp = cdpOf.get(page);
  if (!cdp) {
    cdp = await page.context().newCDPSession(page);
    cdpOf.set(page, cdp);
    await cdp.send("Network.enable");
  }
  await cdp.send("Network.emulateNetworkConditions", NETWORK_PROFILES[profile.network ?? "none"]);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: profile.cpu_throttle ?? 1 });
  return cdp;
}

/**
 * Throttle the context's current pages and every page it opens later (popups).
 * @param {import('playwright').BrowserContext} context
 */
export async function throttleContext(context, profile = {}) {
  if (!throttled(profile)) return;
  context.on("page", (p) => { applyThrottle(p, profile).catch(() => {}); });
  for (const p of context.pages()) await applyThrottle(p, profile);
}
