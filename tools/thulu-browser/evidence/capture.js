// Frames go to disk, not memory: a minute at full viewport is hundreds of MB of JPEG.
// The latest frame is kept between segments because a still screen sends no new frames.

import { writeFileSync } from "node:fs";
import path from "node:path";

import { Reel } from "./reel.js";

const JPEG_QUALITY = Number(process.env.EVIDENCE_JPEG_QUALITY || 88);

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

  /** @param {import('playwright').Page} page  @param {string} dir */
  constructor(page, dir) {
    this.#page = page;
    this.#dir = dir;
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

  async idle(fn) {
    const resume = this.#open;
    if (resume) this.close();
    try {
      return await fn();
    } finally {
      if (resume) this.open(resume.step, resume.label);
    }
  }

  async stop() {
    this.close();
    if (this.#started) {
      this.#started = false;
      await this.#page.screencast.stop().catch(() => {});
    }
    return new Reel(this.#segments);
  }
}

export class NoCapture {
  open() {}
  close() {}
  async film(_step, _label, fn) { return fn(); }
  async idle(fn) { return fn(); }
  async fresh() {}
  async stop() { return new Reel([]); }
}
