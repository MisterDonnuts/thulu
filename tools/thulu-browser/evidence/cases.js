import { t } from "../i18n.js";
import { listTestCases, recordTestCaseRun } from "../lib/octopus.js";
import { judgedRun } from "./report.js";

const OBSERVED_MAX = 500;

function short(text) {
  return text.length > OBSERVED_MAX ? `${text.slice(0, OBSERVED_MAX - 1)}…` : text;
}

function verdictOf({ failed, unreached }) {
  if (failed) return "fail";
  return unreached ? "blocked" : "pass";
}

/**
 * One verdict over every check of the judged run, whatever case each names:
 * how a library run (evidence/job.js) is judged. A take with no checks at
 * all whose steps failed is blocked, since nothing it set out to prove was
 * proven.
 * @returns {"pass"|"fail"|"blocked"}
 */
export function runVerdict(runs) {
  const run = judgedRun(runs);
  const checked = new Set(run.verdicts.map((v) => v.step));
  const planned = run.planned ?? [];
  return verdictOf({
    failed: run.verdicts.some((v) => !v.pass),
    unreached: planned.some((p) => !checked.has(p.step)) ||
      (!planned.length && !run.verdicts.length && run.failures.length > 0),
  });
}

/**
 * The judged run's verdicts grouped by case, in the order the take checked
 * them. A case fails if any of its checks failed; otherwise it is blocked if a
 * failed step kept one of its planned checks from running (a replay records
 * what it meant to check in `planned`; a live take checks as it goes); it
 * passes only when every check ran and passed.
 * @returns {{ key: string, verdict: "pass"|"fail"|"blocked", observed: string }[]}
 */
export function caseVerdicts(runs) {
  const run = judgedRun(runs);
  const byCase = new Map();
  const entry = (key) => byCase.get(key) ?? byCase.set(key, { key, failed: false, unreached: false, observed: [] }).get(key);
  const checked = new Set(run.verdicts.map((v) => v.step));
  for (const p of run.planned ?? []) {
    const key = String(p.case).trim();
    if (key && !checked.has(p.step)) entry(key).unreached = true;
  }
  for (const v of run.verdicts) {
    const key = String(v.case ?? "").trim();
    if (!key) continue;
    const c = entry(key);
    c.failed ||= !v.pass;
    if (v.observed) c.observed.push(String(v.observed));
  }
  const why = run.failures[0]
    ? t("text.case_not_reached_after", { step: run.failures[0].step + 1, error: run.failures[0].error })
    : t("text.case_not_reached");
  return [...byCase.values()].map((c) => {
    const verdict = verdictOf(c);
    const observed = verdict === "blocked" ? [...c.observed, why] : c.observed;
    return { key: c.key, verdict, observed: short(observed.join("; ")) };
  });
}

/**
 * Record one run per case the task has. Never throws: the files and the report
 * are already on the task, so a failure here comes back as `error` next to
 * whatever was recorded before it.
 * @param {{ video?: string, take?: string, report?: string, captures: string[] }} ids  attachment and report ids
 * @returns {Promise<{ recorded: { key: string, verdict: string }[], unknown: string[], error?: string }>}
 */
export async function recordCaseRuns(taskId, runs, ids) {
  const out = { recorded: [], unknown: [] };
  const cases = caseVerdicts(runs);
  if (!cases.length) return out;
  try {
    const known = new Set((await listTestCases(taskId)).map((c) => c.key));
    out.unknown = cases.filter((c) => !known.has(c.key)).map((c) => c.key);
    for (const c of cases.filter((c) => known.has(c.key))) {
      await recordTestCaseRun(taskId, c.key, {
        verdict: c.verdict,
        observed: c.observed,
        video_attachment_id: ids.video || "",
        take_attachment_id: ids.take || "",
        report_id: ids.report || "",
        capture_attachment_ids: ids.captures,
      });
      out.recorded.push({ key: c.key, verdict: c.verdict });
    }
  } catch (e) {
    out.error = e.message;
  }
  return out;
}
