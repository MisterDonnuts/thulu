import { mkdtemp } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

import { t } from "../i18n.js";
import { contactSheet, pickChapters } from "./contact-sheet.js";
import { encode, encodeSideBySide } from "./ffmpeg.js";
import { align, frameAt } from "./reel.js";

export async function workDir() {
  return mkdtemp(path.join(os.tmpdir(), "evidence-"));
}

/**
 * @typedef {{ label?: string, baseUrl: string, reel: import('./reel.js').Reel, shots: any[], verdicts: any[],
 *             notes: any[], failures: any[] }} Run
 * @param {{ title: string, mode: "live"|"replay"|"compare", runs: Run[], take: object,
 *           viewport: {width:number,height:number}, dir: string }} input
 */
export async function makeBundle({ title, mode, runs, take, viewport, dir }) {
  const out = path.join(dir, "evidence.mp4");
  let chapters;
  let video = null;
  let framesAt;

  if (runs.length === 2) {
    const aligned = align(runs[0].reel, runs[1].reel);
    chapters = aligned.chapters;
    if (aligned.left.length) video = await encodeSideBySide(aligned.left, aligned.right, { dir, out, size: viewport });
    framesAt = (t) => [frameAt(aligned.left, t), frameAt(aligned.right, t)];
  } else {
    const { reel } = runs[0];
    chapters = reel.chapters();
    if (!reel.empty) video = await encode(reel.entries(), { dir, out, size: viewport });
    framesAt = (t) => [reel.frameAt(t)];
  }

  const duration = chapters.reduce((s, c) => s + (c.end - c.start), 0);
  const sheet = video
    ? await contactSheet({
        title: `${title} · ${runs.map((r) => r.label).filter(Boolean).join(" | ") || mode}`,
        cells: pickChapters(chapters).map((c) => ({ frames: framesAt((c.start + c.end) / 2), label: c.label, at: c.start })),
      })
    : null;

  return { title, mode, runs, take, viewport, dir, video, chapters, duration, sheet };
}

export function describeBundle(id, b) {
  const verdicts = b.runs.flatMap((r) => r.verdicts.map((v) => ({ ...v, run: r.label })));
  const failures = b.runs.flatMap((r) => r.failures.map((f) => ({ ...f, run: r.label })));
  const shots = b.runs.flatMap((r) => r.shots);
  const lines = [
    `render_id: ${id}`,
    t("text.render_summary", {
      mode: b.mode,
      seconds: (b.duration / 1000).toFixed(1),
      chapters: b.chapters.length,
      captures: shots.length,
    }),
  ];
  if (!b.video) lines.push(t("text.render_no_video"));
  for (const v of verdicts) {
    const verdict = {
      result: v.pass ? "PASS" : "FAIL",
      run: v.run ? `[${v.run}] ` : "",
      case: v.case,
      expected: v.expected,
      observed: v.observed,
    };
    lines.push(v.measured ? t("text.render_verdict", verdict) : t("text.render_verdict_reported", verdict));
  }
  for (const f of failures) {
    lines.push(t("text.render_step_failed", {
      run: f.run ? `[${f.run}] ` : "",
      step: f.step + 1,
      label: f.label || f.kind,
      error: f.error,
    }));
  }
  for (const s of shots) {
    if (s.missing.length) lines.push(t("text.render_shot_missing", { name: s.name, missing: s.missing.join(", ") }));
    if (s.crowded) lines.push(t("text.render_shot_crowded", { name: s.name }));
  }
  lines.push("", t("text.render_next_steps"));
  return lines.join("\n");
}
