import { t } from "../i18n.js";
import { describeFinding } from "../lib/page-findings.js";
import { formatMetric } from "../lib/perf.js";
import { describeProfile, throttled } from "../lib/throttle.js";

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

/**
 * The waiting cut out of a chapter, for its label: " · ⏱ 3.2 s", or both sides of a before/after
 * (" · ⏱ 0.9 s | 3.2 s"). Empty when nothing was cut there.
 */
export function waitTag(c) {
  if (c.waitMs) return t("text.chapter_wait", { wait: formatMetric("response", c.waitMs) });
  if (c.waitLeft || c.waitRight) {
    const side = (ms) => (ms ? formatMetric("response", ms) : "—");
    return t("text.chapter_wait_pair", { left: side(c.waitLeft), right: side(c.waitRight) });
  }
  return "";
}

const chapterLabel = (c) => `${c.label || t("text.report_step", { step: c.step + 1 })}${waitTag(c)}`;

export function chaptersVtt(chapters) {
  const cues = chapters.map((c, i) => `${i + 1}\n${vttTime(c.start)} --> ${vttTime(c.end)}\n${chapterLabel(c)}`);
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
    return `| ${verdictWord(v.pass)} | ${cell(v.case || v.label)} | ${cell(v.expected)} | ${cell(v.observed)}${v.measured ? "" : " ¹"} | ${at ? clock(at.start) : "—"} |`;
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

// For a perf check measured on both sides, the after's difference against the before: "+420 ms".
function perfDelta(l, r) {
  if (!l?.metric || l.metric !== r?.metric || typeof l.value !== "number" || typeof r.value !== "number") return "";
  const d = r.value - l.value;
  const sign = d > 0 ? "+" : d < 0 ? "−" : "±";
  return t("text.report_perf_delta", { delta: `${sign}${formatMetric(l.metric, Math.abs(d))}` });
}

// A check names its case; an accessibility scan may not, and then its label stands in.
const caseKey = (v) => v.case || v.label;

function pairTable(left, right, labels) {
  const cases = [...new Set([...left, ...right].map(caseKey))];
  const find = (list, c) => list.find((v) => caseKey(v) === c);
  const mark = (v) => (v ? `${verdictWord(v.pass)} · ${cell(v.observed)}` : "—");
  return [
    t("text.report_pair_header", { left: labels[0], right: labels[1] }),
    "|---|---|---|---|",
    ...cases.map((c) => {
      const l = find(left, c);
      const r = find(right, c);
      return `| ${cell(c)} | ${cell((l || r).expected)} | ${mark(l)} | ${mark(r)}${perfDelta(l, r)} |`;
    }),
    "",
  ];
}

function stepName(chapters, step) {
  const at = chapterAt(chapters, step);
  return { at: at ? clock(at.start) : "—", label: at?.label || t("text.report_step", { step: step + 1 }) };
}

// What the page complained about, by step. It never changes the verdict: a red page with green
// checks is still green, and the reviewer reads why here.
function pageErrorsSection(runs, chapters, compare) {
  const rows = runs.flatMap((run) => (run.findings ?? []).map((f) => ({ run, f })));
  if (!rows.length) return [];
  const out = [t("text.report_page_errors"), ""];
  for (const { run, f } of rows) {
    const { at, label } = stepName(chapters, f.step);
    out.push(t("text.report_page_error", { at, label: `${compare && run.label ? `${run.label} · ` : ""}${label}`,
      finding: describeFinding(f) }));
  }
  out.push("");
  return out;
}

const value = (m, metric) => (m?.[metric] === null || m?.[metric] === undefined ? "—" : formatMetric(metric, m[metric]));

// Timing shows when the take asked for it: a throttled profile or a perf check. Reports of takes
// that ask for neither stay as they were.
export function timingWanted(runs) {
  const wanted = runs.some((run) => throttled(run.profile) || run.verdicts.some((v) => v.metric));
  return wanted && runs.some((run) => run.metrics?.length);
}

function timingSection(runs, chapters, compare) {
  if (!timingWanted(runs)) return [];
  const out = [t("text.report_timing"), "", t("text.report_timing_note"), ""];
  for (const run of runs) {
    if (!run.metrics?.length) continue;
    if (compare && run.label) out.push(t("text.report_timing_run", { run: run.label }), "");
    out.push(t("text.report_timing_header"), "|---|---|---|---|---|---|---|");
    for (const m of run.metrics) {
      const { label } = stepName(chapters, m.step);
      const response = m.dead ? t("text.metrics_dead") : value(m, "response_ms");
      out.push(`| ${cell(m.label || label)} | ${response} | ${value(m.vitals, "ttfb")} | ${value(m.vitals, "fcp")} | ` +
        `${value(m.vitals, "lcp")} | ${value(m.vitals, "cls")} | ${value(m.vitals, "inp")} |`);
    }
    out.push("");
  }
  return out;
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

// What a blind tester said while working the page out, at the second of the video it said it.
function thinkSection(notes, chapters) {
  const said = notes.filter((n) => n.level === "think");
  if (!said.length) return [];
  return [t("text.report_think"), "",
    ...said.map((n) => t("text.report_think_line", { at: stepName(chapters, n.step).at, text: n.text })), ""];
}

// "· 7.4 s of waiting cut (3)" per run, or that the waits were filmed as they happened.
function waitsNote(runs, compare) {
  const parts = runs.map((run) => {
    const prefix = compare && run.label ? `${run.label}: ` : "";
    if (run.cutWaits === false) return `${prefix}${t("text.report_waits_filmed")}`;
    const cut = (run.gaps ?? []).filter((g) => g.cut);
    if (!cut.length) return "";
    return `${prefix}${t("text.report_waits_cut", { wait: formatMetric("response", cut.reduce((s, g) => s + g.ms, 0)),
      count: cut.length })}`;
  }).filter(Boolean);
  return parts.length ? ` · ${parts.join(" · ")}` : "";
}

function chaptersSection(chapters) {
  if (!chapters.length) return [];
  return [
    `<details><summary>${t("text.report_chapters")}</summary>`,
    "",
    ...chapters.map((c) => `- \`${clock(c.start)}\` ${chapterLabel(c)}`),
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
  const profile = first.profile && Object.keys(first.profile).length ? ` · ${describeProfile(first.profile)}` : "";

  const out = [
    `${where} · ${r.viewport.width}×${r.viewport.height}${profile} · ${clock(r.duration)}${waitsNote(r.runs, Boolean(second))}` +
      ` · ${modeWord(r.mode)}` +
      (verdicts.length
        ? ` · ${second?.label ? `${second.label}: ` : ""}${t("text.report_checks_pass", { passed, count: verdicts.length })}`
        : ""),
    "",
  ];
  if (r.attachments.video) out.push(embed(r.attachments.video), "");
  out.push(...(second ? pairTable(first.verdicts, second.verdicts, [first.label, second.label]) : verdictTable(first.verdicts, r.chapters)));
  out.push(...notesSection(r.runs.flatMap((x) => x.notes), failures));
  out.push(...thinkSection(judgedRun(r.runs).notes, r.chapters));
  out.push(...pageErrorsSection(r.runs, r.chapters, Boolean(second)));
  out.push(...timingSection(r.runs, r.chapters, Boolean(second)));
  out.push(...shotsSection(r.runs.flatMap((x) => x.shots.map((s) => ({ ...s, run: second ? x.label : undefined }))), r.attachments,
    { embed, link }));
  out.push(...chaptersSection(r.chapters));
  const files = [r.attachments.take, r.attachments.chapters, r.attachments.metrics].filter(Boolean).map(link);
  if (files.length) out.push(t("text.report_replay", { files: files.join(" · ") }), "");
  return { title: `${r.title} · ${verdictWord(ok)}`, body: out.join("\n").trimEnd() + "\n" };
}
