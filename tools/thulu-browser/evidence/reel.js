import { t } from "../i18n.js";

/** @typedef {{ file: string, ts: number }} Frame */
/** @typedef {{ step: number, label: string, start: number, end: number, frames: Frame[] }} Segment */
/** @typedef {{ file: string, duration: number, step: number }} Entry  duration in ms */

export class Reel {
  /** @param {Segment[]} segments */
  constructor(segments = []) {
    this.segments = segments.filter((s) => s.frames.length > 0 && s.end > s.start);
  }

  get empty() {
    return this.segments.length === 0;
  }

  get duration() {
    return this.segments.reduce((sum, s) => sum + (s.end - s.start), 0);
  }

  /** Each frame with how long it stays on screen, in output order. @returns {Entry[]} */
  entries() {
    const out = [];
    for (const seg of this.segments) {
      const frames = [...seg.frames].sort((a, b) => a.ts - b.ts);
      frames.forEach((f, i) => {
        const from = Math.max(f.ts, seg.start);
        const until = Math.min(i + 1 < frames.length ? frames[i + 1].ts : seg.end, seg.end);
        if (until > from) out.push({ file: f.file, duration: until - from, step: seg.step });
      });
    }
    return out;
  }

  chapters() {
    const out = [];
    let at = 0;
    for (const seg of this.segments) {
      const len = seg.end - seg.start;
      const last = out[out.length - 1];
      if (last && last.step === seg.step) last.end += len;
      else out.push({ step: seg.step, label: seg.label, start: at, end: at + len });
      at += len;
    }
    return out;
  }

  /** Output ms spent on each step. @returns {Map<number, number>} */
  stepDurations() {
    const out = new Map();
    for (const seg of this.segments) out.set(seg.step, (out.get(seg.step) || 0) + (seg.end - seg.start));
    return out;
  }

  frameAt(t) {
    return frameAt(this.entries(), t);
  }

  lastFrame() {
    return this.entries().at(-1)?.file;
  }
}

/** The frame on screen at output time t (ms) in a list of entries. @param {Entry[]} entries */
export function frameAt(entries, t) {
  let at = 0;
  for (const e of entries) {
    if (t < at + e.duration) return e.file;
    at += e.duration;
  }
  return entries.at(-1)?.file;
}

/**
 * ffmpeg concat-demuxer script for a list of entries. Each image is shown for
 * its duration; the last line is repeated because the demuxer ignores the
 * duration of the final entry.
 * @param {Entry[]} entries
 */
export function concatScript(entries) {
  if (!entries.length) throw new Error(t("errors.nothing_filmed"));
  const quote = (f) => `'${f.replaceAll("'", "'\\''")}'`;
  const lines = ["ffconcat version 1.0"]; // i18n-ignore ffmpeg concat script header
  for (const e of entries) lines.push(`file ${quote(e.file)}`, `duration ${(e.duration / 1000).toFixed(4)}`);
  lines.push(`file ${quote(entries.at(-1).file)}`);
  return lines.join("\n") + "\n";
}

/**
 * Two reels of the same take, played side by side, stay in step: for every
 * step, the shorter side holds its last frame until the longer one finishes.
 * A step one side never reached shows that side frozen where it stopped.
 * @returns {{ left: Entry[], right: Entry[], chapters: {step:number,label:string,start:number,end:number}[] }}
 */
export function align(leftReel, rightReel) {
  const left = leftReel.entries();
  const right = rightReel.entries();
  const labels = new Map([...rightReel.chapters(), ...leftReel.chapters()].map((c) => [c.step, c.label]));
  const steps = [...new Set([...left, ...right].map((e) => e.step))].sort((a, b) => a - b);
  const byStep = (entries) => Map.groupBy(entries, (e) => e.step);
  const L = byStep(left);
  const R = byStep(right);

  const out = { left: [], right: [], chapters: [] };
  let lastL = left[0]?.file ?? right[0].file;
  let lastR = right[0]?.file ?? left[0].file;
  let at = 0;
  for (const step of steps) {
    const l = L.get(step) || [];
    const r = R.get(step) || [];
    const dl = l.reduce((s, e) => s + e.duration, 0);
    const dr = r.reduce((s, e) => s + e.duration, 0);
    const target = Math.max(dl, dr);
    out.left.push(...hold(l, target - dl, lastL, step));
    out.right.push(...hold(r, target - dr, lastR, step));
    lastL = out.left.at(-1).file;
    lastR = out.right.at(-1).file;
    out.chapters.push({ step, label: labels.get(step) || `step ${step + 1}`, start: at, end: at + target });
    at += target;
  }
  return out;
}

function hold(entries, extra, fallbackFile, step) {
  if (!entries.length) return [{ file: fallbackFile, duration: extra, step }];
  if (extra <= 0) return entries;
  const copy = entries.map((e) => ({ ...e }));
  copy.at(-1).duration += extra;
  return copy;
}
