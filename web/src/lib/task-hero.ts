// The top of the task drawer: the ticket key over the title, and the stepper with the columns the
// task goes through. Built as strings because the page repaints them on every poll.

import { t } from "../i18n";
import { historyActor } from "./history";
import { esc, icoSvg } from "./ui";

export interface TitleParts {
  key: string;
  rest: string;
}

// "ABC-123: Remove the screen" reads as the key ABC-123 over "Remove the screen". A title without
// a leading ticket key stays whole.
const KEY = /^([A-Z][A-Z0-9]*-\d+)\s*[:–—-]\s*(\S[\s\S]*)$/;

export function splitTitleKey(title: string): TitleParts {
  const m = (title || "").trim().match(KEY);
  return m ? { key: m[1], rest: m[2].trim() } : { key: "", rest: (title || "").trim() };
}

export function joinTitleKey(key: string, rest: string): string {
  return key ? `${key}: ${rest}` : rest;
}

export interface PathStage {
  id: string;
  label: string;
  initial?: boolean;
  terminal?: boolean;
  // The task skips this column (stage_skips): who set it and why.
  skip?: { reason: string; by: string } | null;
}

export interface PathOptions {
  stages: readonly PathStage[];
  current: string;
  agents?: Record<string, string>;
  running?: boolean;
  // Approvals still ahead of the task, by the stage where they hold it.
  locks?: ReadonlyArray<{ stage?: string; label?: string; key?: string }>;
  compact?: boolean;
}

export interface PathStep {
  id: string;
  label: string;
  state: "done" | "now" | "todo";
  lock: string;
  // The tooltip of a column the task skips; "" when it runs. Never set on the current step.
  skipped: string;
}

// The columns between the first and the last, plus the current one when the task sits in either.
export function pathSteps(o: PathOptions): PathStep[] {
  const list = o.stages.filter((s) => (!s.initial && !s.terminal) || s.id === o.current);
  const at = list.findIndex((s) => s.id === o.current);
  return list.map((s, i) => {
    const lock =
      at >= 0 && i >= at
        ? (o.locks ?? [])
            .filter((l) => l.stage === s.id)
            .map((l) => (l.label ?? "").trim() || (l.key ?? "").trim())
            .filter(Boolean)
            .join(", ")
        : "";
    const state = at < 0 || i > at ? "todo" : i < at ? "done" : "now";
    const label = s.label || s.id;
    const skipped =
      s.skip && state !== "now"
        ? t("tasks.drawer.header.path_skipped", {
            stage: label,
            reason: s.skip.reason,
            by: historyActor(s.skip.by),
          })
        : "";
    return { id: s.id, label, state, lock, skipped };
  });
}

// The current step is a button: it opens the stage picker, as the stage pill did.
export function stagePathHTML(o: PathOptions): string {
  const steps = pathSteps(o);
  const html = steps.map((s, i) => {
    const agent = o.agents?.[s.id] ?? "";
    const title =
      s.skipped ||
      (agent ? t("tasks.drawer.header.path_step", { stage: s.label, agent }) : s.label);
    const lock = s.lock
      ? `<span class="tv-step-lock" title="${esc(t("tasks.drawer.header.path_lock", { label: s.lock }))}">${icoSvg("lock", { label: t("tasks.drawer.header.path_lock", { label: s.lock }) })}</span>`
      : "";
    // The strike-through is not read out: a hidden word says the column is skipped.
    const skipped = s.skipped
      ? `<span class="visually-hidden">${esc(t("tasks.drawer.header.path_skipped_label"))}</span>`
      : "";
    const inner = `<span class="tv-dot" aria-hidden="true"></span><span class="tv-step-label">${esc(s.label)}</span>${skipped}<span class="tv-step-n" aria-hidden="true">${i + 1}</span>${lock}`;
    const cls = [
      "tv-step",
      s.state === "todo" ? "" : s.state,
      s.state === "now" && o.running ? "tone-run" : "",
      s.skipped ? "skipped" : "",
    ]
      .filter(Boolean)
      .join(" ");
    if (s.state === "now")
      return `<button type="button" class="${cls}" id="dr-stage" aria-haspopup="true" aria-current="step" aria-label="${esc(t("tasks.drawer.header.stage_label", { stage: s.label }))}" title="${esc(t("tasks.drawer.header.stage_key"))}">${inner}</button>`;
    return `<span class="${cls}" title="${esc(title)}">${inner}</span>`;
  });
  return `<div class="tv-path${o.compact ? " compact" : ""}" role="group" aria-label="${esc(t("tasks.drawer.header.path_label"))}">${html.join("")}</div>`;
}
