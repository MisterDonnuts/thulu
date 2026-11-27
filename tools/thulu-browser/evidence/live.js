import { mkdir, rm } from "node:fs/promises";
import path from "node:path";

import { t } from "../i18n.js";
import { makeBundle, workDir } from "./bundle.js";
import { FrameCapture } from "./capture.js";
import { Director } from "./director.js";
import { parseTake } from "./schema.js";
import { pointAt } from "./selector.js";

const ACTIONS = new Set(["click", "fill", "type", "select", "check", "press"]);

export class LiveTake {
  #page;
  #dir;
  #capture;
  #director;
  #secrets;
  #chapter = 0;
  #changesData;
  title;
  baseUrl;
  redactCss;
  profile;
  ignoreErrors;
  steps = [];

  constructor({ page, dir, capture, director, secrets, title, baseUrl, redactCss, changesData, profile, ignoreErrors }) {
    this.#page = page;
    this.#dir = dir;
    this.#capture = capture;
    this.#director = director;
    this.#secrets = secrets;
    this.#changesData = changesData;
    this.title = title;
    this.baseUrl = baseUrl;
    this.redactCss = redactCss;
    this.profile = profile;
    this.ignoreErrors = ignoreErrors;
  }

  /** The timing of each action filmed so far. */
  get metrics() {
    return this.#director.metrics;
  }

  /**
   * @param {import('playwright').Page} page
   * @param {{ profile?: object, ignoreErrors?: string[] }} opts  profile: the session's throttling and
   *   color scheme, written into the take so a replay runs under the same conditions
   */
  static async start(page, { title, baseUrl, redactCss = [], changesData = false, profile = {}, ignoreErrors = [],
    cutWaits = true }) {
    const origin = baseUrl || safeOrigin(page.url());
    if (!origin) throw new Error(t("errors.base_url_required"));
    const dir = await workDir();
    const frames = path.join(dir, "a");
    await mkdir(frames, { recursive: true });
    await Director.prepare(page, { redactCss });
    const capture = new FrameCapture(page, frames, { cutWaits });
    await capture.start();
    const secrets = {};
    const director = new Director(page, { capture, baseUrl: origin, secrets });
    const take = new LiveTake({ page, dir, capture, director, secrets, title, baseUrl: origin, redactCss, changesData,
      profile, ignoreErrors });
    if (safeOrigin(page.url())) take.steps.push(take.#portable({ kind: "goto", url: page.url() }));
    return take;
  }

  target(ref) {
    return pointAt(this.#page, ref);
  }

  /**
   * Play a step on camera and write it into the take. A step that fails is
   * not written: the take keeps only what worked.
   * @param {object} step  ready to play; a fill with `secret` names the secret
   * @param {string} [secretValue]  the secret's value, held only while typing
   */
  async perform(step, secretValue) {
    if (step.kind === "fill" && step.secret) this.#secrets[step.secret] = secretValue ?? "";
    try {
      const produced = await this.#director.perform(step, this.#chapter++);
      this.steps.push(this.#portable(step));
      return produced;
    } finally {
      if (step.secret) delete this.#secrets[step.secret];
    }
  }

  #portable(step) {
    if (this.#changesData && ACTIONS.has(step.kind)) step = { ...step, side_effect: true };
    if (step.kind !== "goto") return step;
    try {
      const u = new URL(step.url, this.baseUrl);
      if (u.origin === new URL(this.baseUrl).origin) return { ...step, url: `${u.pathname}${u.search}${u.hash}` };
    } catch {}
    return step;
  }

  toJSON() {
    const vp = this.#page.viewportSize() || { width: 1440, height: 900 };
    return {
      version: 1,
      title: this.title,
      base_url: this.baseUrl,
      viewport: vp,
      ...(this.redactCss.length ? { redact_css: this.redactCss } : {}),
      ...this.profile,
      ...(this.ignoreErrors.length ? { ignore_errors: this.ignoreErrors } : {}),
      ...(this.#capture.cutWaits ? {} : { cut_waits: false }),
      steps: this.steps,
    };
  }

  async finish() {
    const reel = await this.#capture.stop();
    const take = this.steps.length ? parseTake(this.toJSON()) : null;
    const run = {
      baseUrl: this.baseUrl, reel, shots: this.#director.shots, verdicts: this.#director.verdicts,
      notes: this.#director.notes, failures: [], profile: this.profile, metrics: this.#director.metrics,
      findings: this.#director.findings(), cutWaits: this.#capture.cutWaits, gaps: reel.gaps,
    };
    return makeBundle({ title: this.title, mode: "live", runs: [run], take,
      viewport: this.#page.viewportSize() || { width: 1440, height: 900 }, dir: this.#dir });
  }

  async discard() {
    await this.#capture.stop().catch(() => {});
    await rm(this.#dir, { recursive: true, force: true });
  }
}

function safeOrigin(url) {
  try {
    const u = new URL(url);
    return ["http:", "https:"].includes(u.protocol) ? u.origin : "";
  } catch {
    return "";
  }
}
