import { t } from "../i18n.js";

export function clock(ms) {
  const s = Math.max(0, ms) / 1000;
  const m = Math.floor(s / 60);
  return `${m}:${(s - m * 60).toFixed(1).padStart(4, "0")}`;
}

function vttTime(ms) {
  const t = Math.max(0, Math.round(ms));
  const h = String(Math.floor(t / 3_600_000)).padStart(2, "0");
  const m = String(Math.floor(t / 60_000) % 60).padStart(2, "0");
  const s = String(Math.floor(t / 1000) % 60).padStart(2, "0");
  return `${h}:${m}:${s}.${String(t % 1000).padStart(3, "0")}`;
}

export function chaptersVtt(chapters) {
  const cues = chapters.map((c, i) => `${i + 1}\n${vttTime(c.start)} --> ${vttTime(c.end)}\n${c.label || t("text.report_step", { step: c.step + 1 })}`);
  return `WEBVTT\n\n${cues.join("\n\n")}\n`;
}

const cell = (v) => String(v ?? "").replace(/\|/g, "\\|").replace(/\n+/g, " ").trim() || "—";

export const taskAttachmentHref = (a) => `/task-attachments/${a.id}`;

function linker(href = taskAttachmentHref) {
  return {
    embed: (a) => (a ? `![${a.filename}](${href(a)})` : ""),
    link: (a) => (a ? `[${a.filename}](${href(a)})` : ""),
  };
}

export function judgedRun(runs) {
  return runs[1] ?? runs[0];
}

function chapterAt(chapters, step) {
  return chapters.find((c) => c.step === step);
}

const MODE_KEYS = { live: "labels.mode_live", replay: "labels.mode_replay", compare: "labels.mode_compare" };

function modeWord(mode) {
  return MODE_KEYS[mode] ? t(MODE_KEYS[mode]) : mode;
}

function verdictWord(pass) {
  return pass ? t("labels.pass") : t("labels.fail");
}

function verdictTable(verdicts, chapters) {
  if (!verdicts.length) return [];
  const rows = verdicts.map((v) => {
    const at = chapterAt(chapters, v.step);
    return `| ${verdictWord(v.pass)} | ${cell(v.case)} | ${cell(v.expected)} | ${cell(v.observed)}${v.measured ? "" : " ¹"} | ${at ? clock(at.start) : "—"} |`;
  });
  const copied = verdicts.some((v) => !v.measured);
  return [
    t("text.report_verdict_header"),
    "|---|---|---|---|---|",
    ...rows,
    ...(copied ? ["", t("text.report_observed_note")] : []),
    "",
  ];
}

function pairTable(left, right, labels) {
  const cases = [...new Set([...left, ...right].map((v) => v.case))];
  const find = (list, c) => list.find((v) => v.case === c);
  const mark = (v) => (v ? `${verdictWord(v.pass)} · ${cell(v.observed)}` : "—");
  return [
    t("text.report_pair_header", { left: labels[0], right: labels[1] }),
    "|---|---|---|---|",
    ...cases.map((c) => {
      const l = find(left, c);
      const r = find(right, c);
      return `| ${cell(c)} | ${cell((l || r).expected)} | ${mark(l)} | ${mark(r)} |`;
    }),
    "",
  ];
}

function shotsSection(shots, attachments, { embed, link }) {
  if (!shots.length) return [];
  const out = [t("text.report_captures"), ""];
  shots.forEach((s, i) => {
    const a = attachments.shots[i];
    const missing = s.missing.length
      ? t("text.report_shot_missing", { missing: s.missing.map((m) => `\`${m}\``).join(", ") })
      : "";
    out.push(`**${i + 1}. ${cell(s.title)}${s.run ? ` · ${cell(s.run)}` : ""}**${missing}`, "");
    if (a) out.push(embed(a.annotated), "");
    if (a?.clean) out.push(t("text.report_clean", { link: link(a.clean) }), "");
  });
  return out;
}

function notesSection(notes, failures) {
  const facts = notes.filter((n) => n.level === "fact");
  const issues = notes.filter((n) => n.level === "issue");
  const out = [];
  if (issues.length || failures.length) {
    out.push(t("text.report_findings"), "");
    for (const n of issues) out.push(`- ⚠️ ${n.text}`);
    for (const f of failures) {
      out.push(t("text.report_step_failed", { step: f.step + 1, label: f.label || f.kind, error: f.error }));
    }
    out.push("");
  }
  if (facts.length) {
    out.push(t("text.report_facts"), "");
    for (const n of facts) out.push(`- ${n.text}`);
    out.push("");
  }
  return out;
}

function chaptersSection(chapters) {
  if (!chapters.length) return [];
  return [
    `<details><summary>${t("text.report_chapters")}</summary>`,
    "",
    ...chapters.map((c) => `- \`${clock(c.start)}\` ${c.label || t("text.report_step", { step: c.step + 1 })}`),
    "",
    "</details>",
    "",
  ];
}

/**
 * @param {{ title: string, runs: { label?: string, baseUrl: string, verdicts: any[], notes: any[], failures: any[], shots: any[] }[],
 *           chapters: {step:number,label:string,start:number,end:number}[], duration: number,
 *           viewport: {width:number,height:number}, mode: "live"|"replay"|"compare",
 *           attachments: { video?: any, take?: any, chapters?: any, shots: { annotated: any, clean?: any }[] },
 *           href?: (a: {id: string}) => string }} r  href: where a file is served (default: a task attachment)
 * @returns {{ title: string, body: string }}  the Reports card shows the title
 *   as its header, so the body starts at the context line.
 */
export function buildReport(r) {
  const { embed, link } = linker(r.href);
  const [first, second] = r.runs;
  const verdicts = judgedRun(r.runs).verdicts;
  const passed = verdicts.filter((v) => v.pass).length;
  const failures = r.runs.flatMap((x) => x.failures);
  const ok = !(passed < verdicts.length || failures.length);
  const where = r.runs.map((x) => `${x.label ? `${x.label}: ` : ""}\`${x.baseUrl}\``).join(" · ");

  const out = [
    `${where} · ${r.viewport.width}×${r.viewport.height} · ${clock(r.duration)} · ${modeWord(r.mode)}` +
      (verdicts.length
        ? ` · ${second?.label ? `${second.label}: ` : ""}${t("text.report_checks_pass", { passed, count: verdicts.length })}`
        : ""),
    "",
  ];
  if (r.attachments.video) out.push(embed(r.attachments.video), "");
  out.push(...(second ? pairTable(first.verdicts, second.verdicts, [first.label, second.label]) : verdictTable(first.verdicts, r.chapters)));
  out.push(...notesSection(r.runs.flatMap((x) => x.notes), failures));
  out.push(...shotsSection(r.runs.flatMap((x) => x.shots.map((s) => ({ ...s, run: second ? x.label : undefined }))), r.attachments,
    { embed, link }));
  out.push(...chaptersSection(r.chapters));
  const files = [r.attachments.take, r.attachments.chapters].filter(Boolean).map(link);
  if (files.length) out.push(t("text.report_replay", { files: files.join(" · ") }), "");
  return { title: `${r.title} · ${verdictWord(ok)}`, body: out.join("\n").trimEnd() + "\n" };
}
