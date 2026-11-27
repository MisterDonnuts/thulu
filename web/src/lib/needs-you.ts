// The status line under the task's title. The daemon derives `attention` (what a person must do
// now) and `upcoming_approvals` (what a later stage will ask) and returns them with GET /tasks/{id};
// this module only lays them out. An older daemon sends neither field, and the detail then keeps
// the approval banner and the header buttons it always had.

import { t } from "../i18n";
import { formatSeconds } from "./time";
import { agoLabel } from "./task-card";
import type { RunWord } from "./task-detail";
import {
  badgeHTML,
  buttonHTML,
  checkHTML,
  esc,
  fieldErrorHTML,
  fieldHTML,
  icoSvg,
  type BadgeTone,
  type DotTone,
} from "./ui";

export type AttentionKind = "decision" | "approval" | "question" | "failure";

export type AttentionOption =
  "test_by_hand" | "waive_cases" | "send_back" | "answer_question" | "decide_approval" | "rerun";

const OPTIONS: readonly AttentionOption[] = [
  "answer_question",
  "decide_approval",
  "test_by_hand",
  "waive_cases",
  "send_back",
  "rerun",
];

export interface AttentionNeed {
  ref: string;
  title?: string;
  ask?: string;
  why?: string;
}

export interface TaskAttention {
  kind: AttentionKind | string;
  headline?: string;
  stage?: string;
  run_id?: string;
  verdict_lost?: boolean;
  items?: AttentionNeed[] | null;
  options?: string[] | null;
  approval_id?: string;
  question_id?: string;
  // The comment thread a person answers in: the open stop, the approval's thread or the question's
  // comment. Absent on older daemons and older tasks.
  thread_id?: string;
}

export interface UpcomingApproval {
  id: string;
  key?: string;
  label?: string;
  stage?: string;
  stage_label?: string;
  stale?: boolean;
}

// A daemon that knows the contract always sends the `attention` key, null when nothing needs a
// person. Its absence means an older daemon: the detail falls back to today's banner and buttons.
export function hasAttentionContract(task: object | null | undefined): boolean {
  return !!task && Object.hasOwn(task, "attention");
}

// "TC-3", "TC-4", "TC-5", "TC-6", "TC-9" → "TC-3–6, TC-9". Three or more consecutive numbers with
// the same prefix fold into a range; anything that does not end in a number stays as written.
export function refsText(refs: readonly string[]): string {
  const parsed = refs.map((r) => {
    const m = /^(.*?)(\d+)$/.exec(r);
    return m ? { ref: r, prefix: m[1], n: Number(m[2]) } : { ref: r, prefix: null, n: NaN };
  });
  const out: string[] = [];
  let i = 0;
  while (i < parsed.length) {
    const a = parsed[i];
    let j = i;
    while (
      a.prefix !== null &&
      j + 1 < parsed.length &&
      parsed[j + 1].prefix === a.prefix &&
      parsed[j + 1].n === parsed[j].n + 1
    )
      j++;
    if (j - i >= 2) {
      out.push(`${a.ref}–${parsed[j].n}`);
    } else {
      for (let k = i; k <= j; k++) out.push(parsed[k].ref);
    }
    i = j + 1;
  }
  return out.join(", ");
}

// The buttons the card offers. The daemon names them; a daemon that sends none gets the ones its
// kind always allows, so the card never ends up with no way forward.
export function cardOptions(att: TaskAttention): AttentionOption[] {
  const given = (att.options ?? []).filter((o): o is AttentionOption =>
    (OPTIONS as readonly string[]).includes(o),
  );
  const hasItems = (att.items ?? []).length > 0;
  let list: AttentionOption[] = given;
  if (list.length === 0) {
    switch (att.kind) {
      case "question":
        list = att.question_id ? ["answer_question"] : [];
        break;
      case "approval":
        list = ["decide_approval"];
        break;
      case "decision":
        list = hasItems ? ["test_by_hand", "waive_cases", "send_back"] : ["send_back"];
        break;
      default:
        list = ["send_back", "rerun"];
    }
  }
  return OPTIONS.filter((o) => list.includes(o)).filter((o) => {
    if (o === "test_by_hand" || o === "waive_cases") return hasItems;
    if (o === "answer_question") return !!att.question_id;
    return true;
  });
}

export function headlineOf(att: TaskAttention): string {
  const h = (att.headline ?? "").trim();
  if (h) return h;
  switch (att.kind) {
    case "question":
      return t("lib.needs_you.fallback.question");
    case "approval":
      return t("lib.needs_you.fallback.approval");
    case "failure":
      return t("lib.needs_you.fallback.failure");
    default:
      return t("lib.needs_you.fallback.decision");
  }
}

export function needsYouWord(): RunWord {
  return { id: "needs_you", label: t("lib.needs_you.chip"), tone: "warning", icon: "i-flag" };
}

// The run length the "Re-run" item quotes: the median of the finished runs of that stage, so one
// stuck run does not make every rerun look like an hour.
export function typicalSeconds(
  runs: ReadonlyArray<{ stage: string; startedAt: string; finishedAt: string; state: string }>,
  stage: string,
): number {
  const secs = runs
    .filter((r) => r.stage === stage && r.finishedAt && r.state === "done")
    .map((r) => (Date.parse(r.finishedAt) - Date.parse(r.startedAt)) / 1000)
    .filter((s) => isFinite(s) && s > 0)
    .sort((a, b) => a - b);
  if (!secs.length) return 0;
  const mid = Math.floor(secs.length / 2);
  return Math.round(secs.length % 2 ? secs[mid] : (secs[mid - 1] + secs[mid]) / 2);
}

export function rerunLabel(stageLabel: string, seconds: number): string {
  if (seconds < 60) return t("lib.needs_you.actions.rerun", { stage: stageLabel });
  const mins = Math.round(seconds / 60);
  const span = mins < 90 ? formatSeconds(mins * 60).split(" ")[0] : formatSeconds(mins * 60);
  return t("lib.needs_you.actions.rerun_typical", { stage: stageLabel, span });
}

export type CardForm = "" | "waive";

// What the header's status line offers, in order; the first one leads. Send back and Re-run with a
// reason live in the ⋯ menu, which every state has.
export type StatusAction = "missing" | "skip" | "answer" | "approval" | "stop" | "rerun";

export function statusActions(att: TaskAttention, readOnly = false): StatusAction[] {
  const opts = cardOptions(att);
  const out: StatusAction[] = [];
  if (opts.includes("answer_question")) out.push("answer");
  if (opts.includes("decide_approval")) out.push("approval");
  if (opts.includes("test_by_hand")) out.push("missing");
  if (att.thread_id && att.kind !== "question" && att.kind !== "approval") out.push("stop");
  if (!readOnly && opts.includes("waive_cases")) out.push("skip");
  if (!readOnly && opts.includes("rerun") && att.kind === "failure") out.push("rerun");
  return out;
}

export function statusActionLabel(a: Exclude<StatusAction, "rerun">): string {
  switch (a) {
    case "missing":
      return t("lib.needs_you.status.missing");
    case "skip":
      return t("lib.needs_you.status.skip");
    case "answer":
      return t("lib.needs_you.actions.answer");
    case "approval":
      return t("lib.needs_you.status.approval");
    case "stop":
      return t("lib.needs_you.status.stop");
  }
}

// The cases still waiting on a person: the attention's items whose case has not moved on since
// (given access and running again, passed, skipped). The header counts them live.
export function liveNeedCount(
  att: Pick<TaskAttention, "items"> | null | undefined,
  cases?: ReadonlyArray<{ key: string; status?: string }> | null,
): number {
  const refs = (att?.items ?? []).map((i) => i.ref).filter(Boolean);
  if (!cases?.length) return refs.length;
  const status = new Map(cases.map((c) => [c.key, c.status ?? ""]));
  return refs.filter((r) => !status.has(r) || status.get(r) === "blocked").length;
}

export function needsYouPill(count: number): string {
  return count > 0 ? t("lib.needs_you.pill_count", { count }) : t("lib.needs_you.title");
}

export type StatusTone = "warn" | "run" | "danger" | "muted";

export interface StatusLine {
  tone: StatusTone;
  pill: string;
  text: string;
  actionsHTML?: string;
  footHTML?: string;
  formHTML?: string;
}

const PILL_TONE: Record<StatusTone, { badge: BadgeTone; dot: DotTone }> = {
  warn: { badge: "warn", dot: "warn" },
  run: { badge: "info", dot: "info" },
  danger: { badge: "danger", dot: "danger" },
  muted: { badge: "muted", dot: "neutral" },
};

// One line under the title: a pill with the state (and its live number), one sentence on what is
// going on, and what the person can do about it.
export function statusLineHTML(s: StatusLine): string {
  const pill = badgeHTML(s.pill, PILL_TONE[s.tone].badge, {
    shape: "pill",
    dot: PILL_TONE[s.tone].dot,
    pulse: s.tone === "run",
    cls: "ny-pill",
  });
  const acts = s.actionsHTML ? `<div class="ny-acts">${s.actionsHTML}</div>` : "";
  const form = s.formHTML ? `<div class="ny-form-slot">${s.formHTML}</div>` : "";
  return `<section class="ny-status" data-tone="${esc(s.tone)}" aria-label="${esc(t("lib.needs_you.status.label"))}">
      <div class="ny-line">${pill}<p class="ny-msg">${esc(s.text)}</p>${acts}</div>
      ${form}${s.footHTML ?? ""}
    </section>`;
}

export function verdictLostHTML(): string {
  return `<p class="ny-foot">${icoSvg("i-info")}<span>${esc(t("lib.needs_you.verdict_lost"))}</span></p>`;
}

export interface WaiveDraft {
  cases: string[];
  reason: string;
}

export type WaiveProblem = "cases" | "reason";

export function waiveProblem(d: WaiveDraft): WaiveProblem | null {
  if (!d.cases.some((c) => c.trim())) return "cases";
  if (!d.reason.trim()) return "reason";
  return null;
}

export function waiveProblemText(p: WaiveProblem): string {
  return p === "cases" ? t("lib.needs_you.waive.need_case") : t("lib.needs_you.waive.need_reason");
}

// Every needs-person case starts checked: the person unchecks the ones they will test by hand.
export function waiveFormHTML(
  items: readonly AttentionNeed[],
  draft?: Partial<WaiveDraft>,
): string {
  const picked = draft?.cases ? new Set(draft.cases) : null;
  const boxes = items
    .filter((it) => it.ref)
    .map((it) =>
      checkHTML({
        name: "case",
        value: it.ref,
        label: it.title ? `${it.ref} · ${it.title}` : it.ref,
        checked: picked ? picked.has(it.ref) : true,
        cls: "ny-case",
      }),
    )
    .join("");
  return `<form class="ny-form" data-ny-form="waive" novalidate>
      <fieldset class="ny-cases"><legend>${esc(t("lib.needs_you.waive.cases"))}</legend>${boxes}${fieldErrorHTML({ id: "ny-waive-cases-error", cls: "ny-cases-error" })}</fieldset>
      ${fieldHTML({
        label: t("lib.needs_you.waive.reason"),
        for: "ny-waive-reason",
        help: t("lib.needs_you.waive.reason_help"),
        errorId: "ny-waive-reason-error",
        control: `<textarea id="ny-waive-reason" name="reason" rows="2" required aria-describedby="ny-waive-reason-help">${esc(draft?.reason ?? "")}</textarea>`,
      })}
      <div class="ny-form-actions">
        ${buttonHTML({ label: t("lib.needs_you.waive.submit"), size: "sm", variant: "primary", type: "submit" })}
        ${buttonHTML({ label: t("common.buttons.cancel"), size: "sm", variant: "ghost", attrs: { "data-ny-cancel": true } })}
      </div>
    </form>`;
}

export interface StageChoice {
  id: string;
  label: string;
}

// The stages a task can be sent back to: the ones before its current stage in the board's flow.
export function earlierStages(flow: readonly StageChoice[], current: string): StageChoice[] {
  const i = flow.findIndex((s) => s.id === current);
  return i <= 0 ? [] : flow.slice(0, i);
}

// The body of the dialog that asks why a task moves back: the reason travels with the move and is
// posted as a decision, so the earlier stage's agent reads why it got the task back.
export function moveBackReasonHTML(id = "move-back-reason"): string {
  return fieldHTML({
    label: t("lib.needs_you.send_back.note"),
    for: id,
    help: t("lib.needs_you.send_back.note_help"),
    control: `<textarea id="${esc(id)}" name="reason" rows="4" required></textarea>`,
  });
}

// The ⋯ menu's three ways to change where the task goes. Each asks why: the reason travels with
// the request and the daemon posts it in Comments as a decision.
export type ActionKind = "rerun" | "send_back" | "move";

export function actionTitle(kind: ActionKind, stageLabel: string): string {
  switch (kind) {
    case "rerun":
      return t("lib.needs_you.action.rerun_title", { stage: stageLabel });
    case "send_back":
      return t("lib.needs_you.action.send_back_title");
    case "move":
      return t("lib.needs_you.action.move_title");
  }
}

export function actionButton(kind: ActionKind): string {
  switch (kind) {
    case "rerun":
      return t("lib.needs_you.action.rerun");
    case "send_back":
      return t("lib.needs_you.action.send_back");
    case "move":
      return t("lib.needs_you.action.move");
  }
}

function actionPlaceholder(kind: ActionKind): string {
  switch (kind) {
    case "rerun":
      return t("lib.needs_you.action.rerun_example");
    case "send_back":
      return t("lib.needs_you.action.send_back_example");
    case "move":
      return t("lib.needs_you.action.move_example");
  }
}

// The stages each action can go to: back is the ones before the current stage (nearest first
// picked), move is every other stage, re-run stays where it is.
export function actionChoices(
  kind: ActionKind,
  flow: readonly StageChoice[],
  current: string,
): StageChoice[] {
  if (kind === "send_back") return earlierStages(flow, current);
  if (kind === "move") return flow.filter((s) => s.id !== current);
  return [];
}

export function actionFormHTML(
  kind: ActionKind,
  choices: readonly StageChoice[],
  picked?: string,
): string {
  const to =
    picked && choices.some((c) => c.id === picked)
      ? picked
      : kind === "send_back"
        ? (choices[choices.length - 1]?.id ?? "")
        : (choices[0]?.id ?? "");
  const opts = choices
    .map(
      (c) =>
        `<option value="${esc(c.id)}"${c.id === to ? " selected" : ""}>${esc(c.label)}</option>`,
    )
    .join("");
  const stage =
    kind === "rerun"
      ? ""
      : fieldHTML({
          label: t("lib.needs_you.action.to"),
          for: "act-to",
          control: `<select id="act-to" name="stage">${opts}</select>`,
        });
  return (
    stage +
    fieldHTML({
      label: t("lib.needs_you.action.why"),
      for: "act-why",
      help: t("lib.needs_you.action.why_help"),
      control: `<textarea id="act-why" name="reason" rows="4" required placeholder="${esc(actionPlaceholder(kind))}" aria-describedby="act-why-help"></textarea>`,
    })
  );
}

export type ActionProblem = "stage" | "reason";

export function actionProblem(
  kind: ActionKind,
  values: { stage?: unknown; reason?: unknown },
  choices: readonly StageChoice[],
): ActionProblem | null {
  if (kind !== "rerun" && !choices.some((c) => c.id === String(values.stage ?? ""))) return "stage";
  if (!String(values.reason ?? "").trim()) return "reason";
  return null;
}

export function actionProblemText(p: ActionProblem): string {
  return p === "stage"
    ? t("lib.needs_you.action.need_stage")
    : t("lib.needs_you.action.need_reason");
}

// The first value that has text: the daemon may send a field as "" instead of leaving it out.
const filled = (...xs: Array<string | null | undefined>): string => xs.find((x) => !!x) ?? "";

export function upcomingText(a: UpcomingApproval, stageLabel: (id: string) => string): string {
  const label = filled(a.label, a.key);
  const stage = filled(a.stage_label, a.stage ? stageLabel(a.stage) : "");
  return stage ? t("lib.needs_you.later.at", { label, stage }) : label;
}

export interface RunningLine {
  stage: string;
  agent: string;
  startedAt?: string;
}

export function runningLineText(r: RunningLine): string {
  const who = r.agent || t("lib.needs_you.running.someone");
  const ago = r.startedAt ? agoLabel(r.startedAt) : "";
  if (!ago) return t("lib.needs_you.running.line", { stage: r.stage, agent: who });
  if (ago === t("lib.task_card.ago.just_now"))
    return t("lib.needs_you.running.line_now", { stage: r.stage, agent: who });
  return t("lib.needs_you.running.line_ago", { stage: r.stage, agent: who, ago });
}

// Why the person sees the task as blocked, for the rail's "Blocked by" while it holds no
// dependencies: the daemon's headline when it sent one, else the block label.
export function blockedReason(att: TaskAttention | null | undefined, fallback: string): string {
  return att ? headlineOf(att) : fallback;
}
