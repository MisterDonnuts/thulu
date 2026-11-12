import { t } from "../i18n";
import {
  esc,
  icoSvg,
  progressBarHTML,
  progressHTML,
  statusGlyph,
  type Attrs,
  type ProgressSegment,
  type ProgressSize,
} from "./ui";

export interface SubtaskLike {
  id: string;
  title: string;
  stage: string;
  status?: string;
  is_blocked?: boolean;
  blocked_by_count?: number;
  stage_assignments?: Record<string, string>;
}

export type SubtaskState =
  "done" | "running" | "question" | "failed" | "blocked" | "todo" | "active";

const FAILED = new Set([
  "failed",
  "out_of_turns",
  "needs_more_turns",
  "not_started",
  "credential_blocked",
  "no_agent",
  "resume_failed",
]);

export function subtaskState(
  c: SubtaskLike,
  isTerminal: (stage: string) => boolean,
  initial: string,
): SubtaskState {
  if (isTerminal(c.stage)) return "done";
  if (c.status === "running" || c.status === "queued") return "running";
  if (c.status === "awaiting_input") return "question";
  if (FAILED.has(c.status ?? "")) return "failed";
  if (c.is_blocked) return "blocked";
  if (c.stage === initial) return "todo";
  return "active";
}

const STATE_ICON: Record<SubtaskState, string> = {
  done: "i-check-circle",
  running: "",
  question: "i-help",
  failed: "i-x-circle",
  blocked: "i-lock",
  todo: "i-circle",
  active: "i-circle-half",
};

const STATE_LABEL: Record<SubtaskState, string> = {
  done: "lib.subtasks.state.done",
  running: "lib.subtasks.state.running",
  question: "lib.subtasks.state.question",
  failed: "lib.subtasks.state.failed",
  blocked: "lib.subtasks.state.blocked",
  todo: "lib.subtasks.state.todo",
  active: "lib.subtasks.state.active",
};

export function subtaskStateLabel(s: SubtaskState): string {
  return t(STATE_LABEL[s]);
}

export function subtaskRowName(title: string, stateLabel: string, stageLabel: string): string {
  return stateLabel.trim().toLowerCase() === stageLabel.trim().toLowerCase()
    ? t("lib.subtasks.row_name_short", { title, stage: stageLabel })
    : t("lib.subtasks.row_name", { title, state: stateLabel, stage: stageLabel });
}

export function subtaskStateIconHTML(s: SubtaskState): string {
  const icon = STATE_ICON[s];
  return `<span class="st-icon st-${s}" aria-hidden="true">${statusGlyph(icon, "info")}</span>`;
}

export interface SubtaskProgress {
  total: number;
  done: number;
  segments: { done: number; active: number; blocked: number; todo: number };
}

export function subtaskProgress(
  children: SubtaskLike[],
  isTerminal: (stage: string) => boolean,
  initial: string,
): SubtaskProgress {
  const segments = { done: 0, active: 0, blocked: 0, todo: 0 };
  for (const c of children) {
    const s = subtaskState(c, isTerminal, initial);
    if (s === "done") segments.done++;
    else if (s === "blocked") segments.blocked++;
    else if (s === "todo") segments.todo++;
    else segments.active++;
  }
  return { total: children.length, done: segments.done, segments };
}

export function subtaskSegments(p: SubtaskProgress): ProgressSegment[] {
  return [
    { value: p.segments.done, tone: "ok" },
    { value: p.segments.active, tone: "info", outline: true },
    { value: p.segments.blocked, tone: "warn", outline: true },
    { value: p.segments.todo, tone: "track" },
  ];
}

export function subtaskProgressHTML(
  p: SubtaskProgress,
  opts: { text?: "short" | "long"; size?: ProgressSize; cls?: string; attrs?: Attrs } = {},
): string {
  return progressHTML(
    { done: p.done, total: p.total, segments: subtaskSegments(p) },
    {
      text: opts.text ?? "short",
      size: opts.size ?? "xs",
      split: true,
      cls: opts.cls,
      attrs: opts.attrs,
    },
  );
}

export function subtaskBarHTML(p: SubtaskProgress, size: ProgressSize = "sm"): string {
  if (p.total === 0) return "";
  return progressBarHTML(subtaskSegments(p), { size, split: true, decorative: true });
}

const LIST_MARK = /^(?:[-*+]\s+\[[ xX]\]\s*|[-*+]\s+|\d+[.)]\s+|\[[ xX]\]\s*)/;

export function parsePastedList(text: string): string[] {
  const lines = text
    .split(/\r?\n/)
    .map((l) => l.trim().replace(LIST_MARK, "").trim())
    .filter((l) => l.length > 0);
  return lines.length > 1 ? lines : [];
}

export function moveInOrder(ids: string[], id: string, delta: -1 | 1): string[] | null {
  return moveAmong(ids, ids, id, delta);
}

export function moveAmong(
  ids: string[],
  visible: string[],
  id: string,
  delta: -1 | 1,
): string[] | null {
  const v = visible.indexOf(id);
  const neighbour = v < 0 ? undefined : visible[v + delta];
  if (!neighbour || !ids.includes(id) || !ids.includes(neighbour)) return null;
  const out = ids.filter((x) => x !== id);
  out.splice(out.indexOf(neighbour) + (delta === 1 ? 1 : 0), 0, id);
  return out.every((x, k) => x === ids[k]) ? null : out;
}

export function dropInOrder(ids: string[], id: string, before: number): string[] | null {
  const i = ids.indexOf(id);
  if (i < 0) return null;
  const out = ids.filter((x) => x !== id);
  const at = Math.max(0, Math.min(out.length, before > i ? before - 1 : before));
  out.splice(at, 0, id);
  return out.every((x, k) => x === ids[k]) ? null : out;
}

export interface WaitsLine {
  blockers: string[];
  soft: string[];
  ready: boolean;
}

export function waitsLineHTML(state: SubtaskState, w: WaitsLine): string {
  const out: string[] = [];
  if (state === "blocked") {
    if (w.blockers.length === 0)
      out.push(`<span class="st-wait">${esc(t("lib.subtasks.waits_unknown"))}</span>`);
    else
      out.push(
        `<span class="st-wait">${icoSvg("lock")}${esc(
          w.blockers.length === 1
            ? t("lib.subtasks.waits_for", { title: w.blockers[0] })
            : t("lib.subtasks.waits_for_more", {
                title: w.blockers[0],
                count: w.blockers.length - 1,
              }),
        )}</span>`,
      );
  } else if (state === "todo" && w.ready) {
    out.push(`<span class="st-ready">${esc(t("lib.subtasks.ready"))}</span>`);
  }
  if (state !== "done" && w.soft.length > 0) {
    const text =
      w.soft.length === 1
        ? t("lib.subtasks.after", { title: w.soft[0] })
        : t("lib.subtasks.after_more", { title: w.soft[0], count: w.soft.length - 1 });
    out.push(`<span class="st-after">${esc(text)}</span>`);
  }
  return out.join("");
}

export interface StartStage {
  id: string;
  label: string;
  terminal?: boolean;
}

export interface StartTarget {
  stage: string;
  label: string;
  runs: boolean;
}

export function startTarget(
  flow: StartStage[],
  current: string,
  assignments: Record<string, string>,
): StartTarget | null {
  const i = flow.findIndex((s) => s.id === current);
  if (i < 0) return null;
  const later = flow.slice(i + 1).filter((s) => !s.terminal);
  const staffed = later.find((s) => assignments[s.id]);
  if (staffed)
    return {
      stage: staffed.id,
      label: t("lib.subtasks.start_in", { stage: staffed.label }),
      runs: true,
    };
  if (i + 1 >= flow.length) return null;
  const next = flow[i + 1];
  return {
    stage: next.id,
    label: t("lib.task_detail.move_to", { stage: next.label }),
    runs: false,
  };
}
