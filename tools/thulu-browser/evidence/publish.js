import { readFile } from "node:fs/promises";

import { t } from "../i18n.js";
import { postReport, uploadAttachment } from "../lib/octopus.js";
import { recordCaseRuns } from "./cases.js";
import { buildReport, chaptersVtt, judgedRun } from "./report.js";

export function slug(text, fallback = "evidence") {
  const s = String(text || "")
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 60);
  return s || fallback;
}

/**
 * Upload every file of a render through `upload`, in a fixed order (captures,
 * video, chapters, take), and return what the report links to. Shared by a
 * task publish and a library run (evidence/job.js), which differ only in
 * where the bytes go.
 * @param {Awaited<ReturnType<import('./bundle.js').makeBundle>>} b
 * @param {(file: { kind: "shot"|"clean"|"video"|"chapters"|"take", filename: string, body: Buffer, mime: string })
 *          => Promise<{ id: string, filename: string } | undefined>} upload  undefined leaves the file out
 * @param {{ clean?: boolean }} opts  clean=false skips the unannotated captures
 * @returns {Promise<{ attachments: { video?: any, chapters?: any, take?: any, shots: { annotated?: any, clean?: any }[] },
 *                     captures: string[] }>}  captures: the judged run's annotated capture ids
 */
export async function uploadBundle(b, upload, { clean = true } = {}) {
  const base = slug(b.title);
  const judged = judgedRun(b.runs);
  const shots = [];
  const captures = [];
  let n = 0;
  for (const run of b.runs) {
    const prefix = run.label ? `${slug(run.label)}-` : "";
    for (const s of run.shots) {
      const name = `${base}-${String(++n).padStart(2, "0")}-${prefix}${slug(s.name, "capture")}`;
      const shot = {
        annotated: await upload({ kind: "shot", filename: `${name}.png`, body: s.png, mime: "image/png" }),
        clean: clean && s.clean
          ? await upload({ kind: "clean", filename: `${name}-clean.png`, body: s.clean, mime: "image/png" })
          : undefined,
      };
      shots.push(shot);
      if (run === judged && shot.annotated) captures.push(shot.annotated.id);
    }
  }
  const video = b.video
    ? await upload({ kind: "video", filename: `${base}.mp4`, body: await readFile(b.video), mime: "video/mp4" })
    : undefined;
  const chapters = b.chapters.length
    ? await upload({ kind: "chapters", filename: `${base}.chapters.vtt`, body: Buffer.from(chaptersVtt(b.chapters)), mime: "text/vtt" })
    : undefined;
  const take = b.take
    ? await upload({ kind: "take", filename: `${base}.take.json`, body: Buffer.from(JSON.stringify(b.take, null, 2)),
      mime: "application/json" })
    : undefined;
  return { attachments: { video, chapters, take, shots }, captures };
}

/**
 * @param {Awaited<ReturnType<import('./bundle.js').makeBundle>>} b
 * @param {string} taskId
 * @param {{ recordRuns?: boolean }} opts  recordRuns=false leaves the task's test cases alone.
 */
export async function publishBundle(b, taskId, { recordRuns = true } = {}) {
  if (!taskId) throw new Error(t("errors.publish_task_required"));
  const { attachments, captures } = await uploadBundle(b, async (f) => {
    const att = await uploadAttachment(taskId, f.filename, f.body, f.mime);
    return { id: att.id, filename: f.filename };
  });
  const { video, take } = attachments;
  const report = buildReport({ title: b.title, runs: b.runs, chapters: b.chapters, duration: b.duration,
    viewport: b.viewport, mode: b.mode, attachments });
  const posted = await postReport(taskId, { kind: "evidence", title: report.title, body: report.body });
  const cases = recordRuns
    ? await recordCaseRuns(taskId, b.runs, { video: video?.id, take: take?.id, report: posted.id, captures })
    : undefined;
  return { report_id: posted.id, attachments, report, cases };
}
