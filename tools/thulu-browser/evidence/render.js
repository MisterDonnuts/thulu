import { mkdir, rm } from "node:fs/promises";
import path from "node:path";

import { t } from "../i18n.js";
import { getBrowser } from "../lib/browser.js";
import { FrameCapture } from "./capture.js";
import { Director, GuardError } from "./director.js";
import { ffmpegAvailable } from "./ffmpeg.js";
import { makeBundle, workDir } from "./bundle.js";
import { compileSteps } from "./library.js";
import { TakeError } from "./schema.js";

const MAX_CONCURRENT = Number(process.env.EVIDENCE_MAX_RENDERS || 2);

class Slots {
  #free;
  #queue = [];
  constructor(n) { this.#free = n; }
  async run(fn) {
    if (this.#free > 0) this.#free--;
    else await new Promise((r) => this.#queue.push(r));
    try { return await fn(); } finally {
      const next = this.#queue.shift();
      if (next) next(); else this.#free++;
    }
  }
}
const slots = new Slots(MAX_CONCURRENT);

function rebase(take, baseUrl) {
  return baseUrl ? new URL(baseUrl).toString().replace(/\/$/, "") : take.base_url;
}

function allowedHosts(take, baseUrls) {
  if (take.allowed_hosts?.length) return [...take.allowed_hosts, ...baseUrls.map((u) => new URL(u).host)];
  return baseUrls.map((u) => new URL(u).host);
}

async function playOnce(take, steps, { baseUrl, badge, pace, secrets, dir, hosts }) {
  await mkdir(dir, { recursive: true });
  const browser = await getBrowser();
  const context = await browser.newContext({
    viewport: take.viewport,
    timezoneId: take.timezone,
    locale: take.locale,
  });
  try {
    const page = await context.newPage();
    await Director.prepare(page, { redactCss: take.redact_css, badge });
    const capture = new FrameCapture(page, dir);
    await capture.start();
    const director = new Director(page, { capture, pace, baseUrl, secrets, allowedHosts: hosts });
    const failures = [];
    for (const [i, step] of steps.entries()) {
      try {
        await director.perform(step, i);
      } catch (err) {
        failures.push({ step: i, kind: step.kind, label: step.label, error: err.message.split("\n")[0] });
        if (err instanceof GuardError) break;
      }
    }
    const reel = await capture.stop();
    const planned = steps.flatMap((s, i) => (s.kind === "verify" && s.case ? [{ step: i, case: s.case }] : []));
    return { label: badge, baseUrl, reel, shots: director.shots, verdicts: director.verdicts,
      notes: director.notes, failures, planned };
  } finally {
    await context.close().catch(() => {});
  }
}

/**
 * Replay a parsed take and produce a bundle. With compareBaseUrl, the take runs
 * twice and the bundle is a side-by-side video.
 * @param {object} take parsed (schema.parseTake)
 * @param {{ library: object, secrets?: Record<string,string>, baseUrl?: string, compareBaseUrl?: string,
 *           labels?: [string, string], pace?: number, allowSideEffects?: boolean }} opts
 */
export async function renderTake(take, opts) {
  const steps = compileSteps(take.steps, opts.library);
  const effects = steps.filter((s) => s.side_effect);
  if (effects.length && !opts.allowSideEffects) {
    throw new TakeError(t("errors.take_changes_data"), [
      ...effects.map((s) => `${s.kind}: ${s.label || s.target || ""}`),
      t("errors.take_changes_data_hint"),
    ]);
  }
  if (!(await ffmpegAvailable())) throw new Error(t("errors.no_ffmpeg"));

  const first = rebase(take, opts.baseUrl);
  const second = opts.compareBaseUrl ? rebase(take, opts.compareBaseUrl) : null;
  const hosts = allowedHosts(take, [first, second].filter(Boolean));
  const labels = opts.labels ?? [t("labels.before"), t("labels.after")];
  const dir = await workDir();

  try {
    return await slots.run(async () => {
      // Each run needs its own folder: frame files are numbered per capture and would overwrite
      // each other.
      const common = { pace: opts.pace ?? 1, secrets: opts.secrets ?? {}, hosts };
      const runs = [await playOnce(take, steps, { ...common, dir: path.join(dir, "a"), baseUrl: first,
        badge: second ? labels[0] : "" })];
      if (second) {
        runs.push(await playOnce(take, steps, { ...common, dir: path.join(dir, "b"), baseUrl: second, badge: labels[1] }));
      }
      return makeBundle({ title: take.title, mode: second ? "compare" : "replay", runs, take, viewport: take.viewport, dir });
    });
  } catch (err) {
    await rm(dir, { recursive: true, force: true });
    throw err;
  }
}
