// Per-task column skips (task stage_skips): which columns a task may skip, the ones it skips ahead
// of it, and the drawer's editor. skippableReason mirrors the daemon's SkippableReason
// (internal/stage/skippable.go), so the editor locks the columns the daemon refuses.

import { t } from "../i18n";
import { historyActor } from "./history";
import type { Stage } from "./stages";
import { calloutHTML, checkHTML, esc, fieldHTML } from "./ui";

export interface StageSkip {
  stage: string;
  reason: string;
  // auth.Author.Label() of who set it: "agent:thulu-refiner", an email, "key:<name>".
  by: string;
  at: string;
}

export type SkipWhy =
  "" | "initial" | "terminal" | "virtual" | "no_skip" | "refinement" | "approvals" | "pull_request";

type SkipFlags = Pick<
  Stage,
  | "initial"
  | "terminal"
  | "virtual"
  | "ingest_flags"
  | "open_pr"
  | "no_skip"
  | "required_approvals"
  | "pr_draft"
  | "pr_ready"
  | "merge_target"
  | "release_resources"
>;

// "" when a task may skip the column, else why not. Same order as the daemon's switch.
export function skippableReason(s: Partial<SkipFlags>): SkipWhy {
  if (s.initial) return "initial";
  if (s.terminal) return "terminal";
  if (s.virtual) return "virtual";
  if (s.no_skip) return "no_skip";
  if (s.ingest_flags) return "refinement";
  if (s.required_approvals?.length) return "approvals";
  if (s.open_pr || s.pr_draft || s.pr_ready || s.merge_target || s.release_resources)
    return "pull_request";
  return "";
}

export function skipWhyText(why: SkipWhy): string {
  switch (why) {
    case "initial":
      return t("lib.stage_skips.why.initial");
    case "terminal":
      return t("lib.stage_skips.why.terminal");
    case "virtual":
      return t("lib.stage_skips.why.virtual");
    case "no_skip":
      return t("lib.stage_skips.why.no_skip");
    case "refinement":
      return t("lib.stage_skips.why.refinement");
    case "approvals":
      return t("lib.stage_skips.why.approvals");
    case "pull_request":
      return t("lib.stage_skips.why.pull_request");
    case "":
      return "";
  }
}

export interface Skipping {
  stage_skips?: StageSkip[] | null;
}

// The task's skip of this column, or null. A skip on a column the board made unskippable since is
// ignored, as the daemon ignores it when it advances.
export function activeSkip(
  task: Skipping,
  stage: (Partial<SkipFlags> & { id: string }) | undefined,
): StageSkip | null {
  if (!stage || skippableReason(stage) !== "") return null;
  return (task.stage_skips ?? []).find((s) => s.stage === stage.id) ?? null;
}

// The columns after the current one that the task skips, in board order.
export function skipsAhead<S extends Partial<SkipFlags> & { id: string }>(
  task: Skipping & { stage: string },
  flow: readonly S[],
): S[] {
  const at = flow.findIndex((s) => s.id === task.stage);
  if (at < 0) return [];
  return flow.slice(at + 1).filter((s) => activeSkip(task, s) !== null);
}

// The ids of every column the task skips, for the next-column helpers.
export function skippedIDs<S extends Partial<SkipFlags> & { id: string }>(
  task: Skipping,
  flow: readonly S[],
): Set<string> {
  return new Set(flow.filter((s) => activeSkip(task, s) !== null).map((s) => s.id));
}

// One column in the editor. "ahead" may be skipped or run again; "locked" can't be skipped (why
// says so); "current" is where the task sits; "passed" is a column the task skipped and went past,
// which can only run again (the task goes back there).
export type SkipRowKind = "ahead" | "locked" | "current" | "passed";

export interface SkipRow {
  stage: string;
  label: string;
  kind: SkipRowKind;
  skip: StageSkip | null;
  why: SkipWhy;
}

// The columns the stepper shows, from the first after the start up to the last before the finish.
// A task in a finishing column has nothing left to edit.
export function skipEditorRows(
  task: Skipping & { stage: string },
  flow: readonly Stage[],
): SkipRow[] {
  const at = flow.findIndex((s) => s.id === task.stage);
  if (at < 0 || flow[at].terminal) return [];
  const rows: SkipRow[] = [];
  flow.forEach((s, i) => {
    if (s.initial || s.terminal || s.virtual) return;
    const skip = activeSkip(task, s);
    const base = { stage: s.id, label: s.label || s.id, skip, why: skippableReason(s) };
    if (i < at) {
      if (skip) rows.push({ ...base, kind: "passed" });
    } else if (i === at) rows.push({ ...base, kind: "current" });
    else rows.push({ ...base, kind: base.why === "" ? "ahead" : "locked" });
  });
  return rows;
}

// Whether the editor has anything a person can change.
export function skipsEditable(rows: readonly SkipRow[]): boolean {
  return rows.some((r) => r.kind === "ahead" || r.kind === "passed");
}

// Must match the daemon's MaxSkipReason (runes, after trimming).
export const MAX_SKIP_REASON = 500;

const toggleName = (r: SkipRow) => (r.kind === "passed" ? `run:${r.stage}` : `skip:${r.stage}`);
const reasonName = (r: SkipRow) => `reason:${r.stage}`;
const reasonID = (r: SkipRow) => `skip-reason-${r.stage}`;

function skipHint(skip: StageSkip): string {
  return t("lib.stage_skips.editor.skipped_by", { by: historyActor(skip.by), reason: skip.reason });
}

function rowHTML(r: SkipRow): string {
  const id = `skip-${r.stage}`;
  const toggleAttrs = { "data-skip-toggle": r.stage };
  if (r.kind === "current" || r.kind === "locked") {
    return checkHTML({
      id,
      label: t("lib.stage_skips.editor.skip", { stage: r.label }),
      disabled: true,
      reason: r.kind === "current" ? t("lib.stage_skips.editor.why_current") : skipWhyText(r.why),
    });
  }
  const passed = r.kind === "passed";
  const check = checkHTML({
    id,
    name: toggleName(r),
    label: passed
      ? t("lib.stage_skips.editor.run_again", { stage: r.label })
      : t("lib.stage_skips.editor.skip", { stage: r.label }),
    hint: r.skip ? skipHint(r.skip) : undefined,
    checked: !passed && r.skip !== null,
    inputAttrs: toggleAttrs,
  });
  const reasonLabel = passed
    ? t("lib.stage_skips.editor.reason_again", { stage: r.label })
    : r.skip
      ? t("lib.stage_skips.editor.reason_run", { stage: r.label })
      : t("lib.stage_skips.editor.reason_skip", { stage: r.label });
  const field = fieldHTML({
    label: reasonLabel,
    for: reasonID(r),
    errorId: `${reasonID(r)}-error`,
    control: `<input id="${esc(reasonID(r))}" name="${esc(reasonName(r))}" maxlength="${MAX_SKIP_REASON}" autocomplete="off" required>`,
    cls: "skip-reason",
    attrs: { "data-skip-reason": r.stage, hidden: true },
  });
  const warn = passed
    ? calloutHTML({
        tone: "warn",
        icon: "warn",
        body: t("lib.stage_skips.editor.back_warning", { stage: r.label }),
        cls: "skip-back",
        attrs: { "data-skip-back": r.stage, hidden: true },
      })
    : "";
  return `<div class="skip-row" data-skip-row="${esc(r.stage)}">${check}${field}${warn}</div>`;
}

export function skipEditorHTML(rows: readonly SkipRow[]): string {
  return `<div class="skip-editor form-stack">${rows.map(rowHTML).join("")}</div>`;
}

// A row asks for a reason once its toggle differs from what the task has now.
export function bindSkipEditor(root: HTMLElement): void {
  root.addEventListener("change", (e) => {
    const input = e.target as HTMLInputElement;
    const stage = input.dataset.skipToggle;
    if (!stage) return;
    const row = input.closest<HTMLElement>("[data-skip-row]");
    if (!row) return;
    const changed = input.checked !== input.defaultChecked;
    const field = row.querySelector<HTMLElement>("[data-skip-reason]");
    if (field) field.hidden = !changed;
    const back = row.querySelector<HTMLElement>("[data-skip-back]");
    if (back) back.hidden = !changed;
    if (changed) field?.querySelector<HTMLInputElement>("input")?.focus();
  });
}

export interface SkipChange {
  op: "add" | "remove";
  stage: string;
  reason: string;
  // Removing it sends the task back to the column.
  back: boolean;
}

// What the person changed, from the dialog's values. missing lists the changed rows without a
// reason, in the order they show.
export function skipChanges(
  rows: readonly SkipRow[],
  values: Record<string, string | boolean>,
): { changes: SkipChange[]; missing: string[] } {
  const changes: SkipChange[] = [];
  const missing: string[] = [];
  for (const r of rows) {
    if (r.kind !== "ahead" && r.kind !== "passed") continue;
    const on = values[toggleName(r)] === true;
    let op: SkipChange["op"] | "" = "";
    if (r.kind === "passed") op = on ? "remove" : "";
    else if (on && !r.skip) op = "add";
    else if (!on && r.skip) op = "remove";
    if (!op) continue;
    const reason = String(values[reasonName(r)] ?? "").trim();
    if (!reason) missing.push(r.stage);
    changes.push({ op, stage: r.stage, reason, back: r.kind === "passed" });
  }
  return { changes, missing };
}

// The field that holds a row's reason, for an error under it.
export function skipReasonInput(root: ParentNode, stage: string): HTMLInputElement | null {
  return root.querySelector<HTMLInputElement>(`#skip-reason-${CSS.escape(stage)}`);
}
