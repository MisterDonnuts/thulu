// Frames go to disk, not memory: a minute at full viewport is hundreds of MB of JPEG.
// The latest frame is kept between segments because a still screen sends no new frames.

import { writeFileSync } from "node:fs";
import path from "node:path";

import { Reel } from "./reel.js";

const JPEG_QUALITY = Number(process.env.EVIDENCE_JPEG_QUALITY || 88);
// A wait shorter than this gets no mark: the staging pauses are of that order.
export const GAP_MIN_MS = Number(process.env.EVIDENCE_GAP_MIN_MS || 800);
// What the app made the viewer wait for. Holds and scripts are the take's own staging, never the
// app's, so they are cut without a mark even when waits are filmed.
export const WAIT_REASONS = new Set(["nav", "settle", "wait", "check"]);

export class FrameCapture {
  #page;
  #dir;
  #n = 0;
  #latest = null;
  #open = null;
  #segments = [];
  #started = false;
  #seen = 0;
  #waiters = [];
  #cutWaits;
  #last = { step: 0, label: "" };
  #gaps = [];
  #pending = 0;

  /**
   * @param {import('playwright').Page} page  @param {string} dir
   * @param {{ cutWaits?: boolean }} [opts]  cutWaits=false films the app's waits in real time
   *   instead of cutting them
   */
  constructor(page, dir, { cutWaits = true } = {}) {
    this.#page = page;
    this.#dir = dir;
    this.#cutWaits = cutWaits;
  }

  get cutWaits() {
    return this.#cutWaits;
  }

  /** Every wait of at least GAP_MIN_MS so far: {step, ms, reason, cut}. */
  get gaps() {
    return this.#gaps;
  }

  /** The step now playing: a wait before its first filmed frame (a slow goto) belongs to it. */
  begin(step, label) {
    if (!this.#open) this.#last = { step, label: label || "" };
  }

  /** The waiting cut since the last call, in ms, so the next filmed moment can say so. */
  takeCut() {
    const ms = this.#pending;
    this.#pending = 0;
    return ms;
  }

  async start() {
    const vp = this.#page.viewportSize() || { width: 1440, height: 900 };
    await this.#page.screencast.start({
      size: vp,
      quality: JPEG_QUALITY,
      onFrame: ({ data, timestamp }) => this.#onFrame(data, timestamp),
    });
    this.#started = true;
  }

  #write(data) {
    const file = path.join(this.#dir, `f${String(++this.#n).padStart(6, "0")}.jpg`);
    writeFileSync(file, data);
    return file;
  }

  #onFrame(data, ts) {
    this.#latest = { data, ts };
    this.#seen++;
    if (this.#open) this.#open.frames.push({ file: this.#write(data), ts });
    for (const wake of this.#waiters.splice(0)) wake();
  }

  // Screencast sends a frame for a single change late, or never when a screenshot takes the same
  // paint;
  // if nothing arrives, force an invisible repaint and wait once more.
  async fresh(timeoutMs = 700) {
    if (!this.#started) return;
    for (let attempt = 0; attempt < 2; attempt++) {
      const before = this.#seen;
      await this.#page.evaluate((nudge) => new Promise((done) => {
        if (nudge) {
          const n = document.createElement("div");
          n.style.cssText = "position:fixed;left:0;top:0;width:1px;height:1px;opacity:.01;pointer-events:none"; // i18n-ignore CSS
          document.documentElement.appendChild(n);
          requestAnimationFrame(() => requestAnimationFrame(() => { n.remove(); done(); }));
        } else requestAnimationFrame(() => requestAnimationFrame(() => done()));
      }), attempt > 0).catch(() => {});
      const arrived = await new Promise((resolve) => {
        if (this.#seen > before) { resolve(true); return; }
        const timer = setTimeout(() => resolve(false), timeoutMs);
        this.#waiters.push(() => { clearTimeout(timer); resolve(true); });
      });
      if (arrived) return;
    }
  }

  open(step, label) {
    if (this.#open) return;
    this.#last = { step, label: label || "" };
    const now = Date.now();
    this.#open = { step, label: label || "", start: now, end: now, frames: [] };
    if (this.#latest) this.#open.frames.push({ file: this.#write(this.#latest.data), ts: now });
  }

  close() {
    if (!this.#open) return;
    this.#open.end = Date.now();
    this.#segments.push(this.#open);
    this.#open = null;
  }

  async film(step, label, fn) {
    const owner = !this.#open;
    if (owner) this.open(step, label);
    try {
      return await fn();
    } finally {
      if (owner) this.close();
    }
  }

  /**
   * Run fn off camera. With a wait reason, a wait of at least GAP_MIN_MS is recorded; when waits are
   * filmed (cutWaits=false) fn runs on camera instead, in a segment of its own under the last step
   * filmed when none is open (the settle after a click runs between two segments).
   * @param {() => Promise<any>} fn
   * @param {{ reason?: string, waited?: (result: any, startedAt: number) => number }} [opts]
   *   waited: how much of the time was the app's, when part of it was the tool's own quiet window
   */
  async idle(fn, { reason, waited } = {}) {
    const wait = WAIT_REASONS.has(reason);
    const filmed = wait && !this.#cutWaits;
    const resume = this.#open;
    const own = filmed && !resume;
    if (own) this.open(this.#last.step, this.#last.label);
    else if (!filmed && resume) this.close();
    const step = resume?.step ?? this.#last.step;
    const startedAt = Date.now();
    try {
      const out = await fn();
      if (wait) this.#gap(step, reason, Math.round(waited ? waited(out, startedAt) : Date.now() - startedAt), !filmed);
      return out;
    } finally {
      if (own) this.close();
      else if (!filmed && resume) this.open(resume.step, resume.label);
    }
  }

  #gap(step, reason, ms, cut) {
    if (!(ms >= GAP_MIN_MS)) return;
    this.#gaps.push({ step, ms, reason, cut });
    if (cut) this.#pending += ms;
  }

  async stop() {
    this.close();
    if (this.#started) {
      this.#started = false;
      await this.#page.screencast.stop().catch(() => {});
    }
    return new Reel(this.#segments, this.#gaps);
  }
}

export class NoCapture {
  cutWaits = true;
  gaps = [];
  takeCut() { return 0; }
  begin() {}
  open() {}
  close() {}
  async film(_step, _label, fn) { return fn(); }
  async idle(fn) { return fn(); }
  async fresh() {}
  async stop() { return new Reel([]); }
}
