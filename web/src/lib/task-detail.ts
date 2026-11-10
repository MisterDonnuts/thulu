import { t } from "../i18n";
import { estimatedCost, fmtDecimal, fmtUsd } from "./format";
import { agoLabel } from "./task-card";
import {
  attrsHTML,
  badgeHTML,
  buttonHTML,
  calloutHTML,
  esc,
  priorityTagHTML,
  type Attrs,
  type BadgeTone,
  type CalloutTone,
  type PriorityTagOptions,
} from "./ui";

export type Tone = "info" | "muted" | "warning" | "danger" | "success";

export interface RunWord {
  id: string;
  label: string;
  tone: Tone;
  icon: string;
}

export function runWord(status: string | undefined, terminal: boolean): RunWord | null {
  if (terminal) {
    return { id: "done", label: t("lib.task_detail.run.done"), tone: "success", icon: "i-check" };
  }
  switch (status ?? "") {
    case "running":
      return { id: "running", label: t("lib.task_detail.run.running"), tone: "info", icon: "" };
    case "queued":
      return {
        id: "queued",
        label: t("lib.task_detail.run.queued"),
        tone: "muted",
        icon: "i-clock",
      };
    case "awaiting_input":
      return {
        id: "needs_answer",
        label: t("lib.task_detail.run.needs_answer"),
        tone: "warning",
        icon: "i-help",
      };
    case "awaiting_approval":
      return {
        id: "needs_approval",
        label: t("lib.task_detail.run.needs_approval"),
        tone: "warning",
        icon: "i-flag",
      };
    case "failed":
      return {
        id: "failed",
        label: t("lib.task_detail.run.failed"),
        tone: "danger",
        icon: "i-x-circle",
      };
    case "out_of_turns":
    case "needs_more_turns":
      return {
        id: "out_of_turns",
        label: t("lib.task_detail.run.out_of_turns"),
        tone: "danger",
        icon: "i-alert",
      };
    case "blocked":
      return {
        id: "blocked_auditor",
        label: t("lib.task_detail.run.blocked_auditor"),
        tone: "danger",
        icon: "i-alert",
      };
    case "not_started":
      return {
        id: "not_started",
        label: t("lib.task_detail.run.not_started"),
        tone: "danger",
        icon: "i-x-circle",
      };
    case "credential_blocked":
      return {
        id: "credential_blocked",
        label: t("lib.task_detail.run.credential_blocked"),
        tone: "danger",
        icon: "i-lock",
      };
    case "no_agent":
      return {
        id: "no_agent",
        label: t("lib.task_detail.run.no_agent"),
        tone: "warning",
        icon: "i-bot",
      };
    case "stopped":
      return {
        id: "stopped",
        label: t("lib.task_detail.run.stopped"),
        tone: "muted",
        icon: "i-power",
      };
    case "resume_failed":
      return {
        id: "resume_failed",
        label: t("lib.task_detail.run.resume_failed"),
        tone: "danger",
        icon: "i-alert",
      };
    default:
      return null;
  }
}

const RUN_BADGE_TONE: Record<Tone, BadgeTone> = {
  info: "info",
  muted: "muted",
  warning: "warn",
  danger: "danger",
  success: "ok",
};

export function runBadgeTone(tone: Tone): BadgeTone {
  return RUN_BADGE_TONE[tone];
}

export interface RunPillOptions {
  title?: string;
  cls?: string;
  attrs?: Attrs;
}

export function runPillHTML(w: RunWord | null, reason = "", opts: RunPillOptions = {}): string {
  if (!w) return "";
  const title = opts.title ?? (reasonLine(reason) || undefined);
  return badgeHTML(w.label, runBadgeTone(w.tone), {
    shape: "pill",
    icon: w.icon || undefined,
    pulse: !w.icon,
    title,
    cls: opts.cls,
    attrs: { "data-run": w.id, ...opts.attrs },
  });
}

// Daemon notes put the failure on the last line, except refused launches, which list
// each item and end with the fix.
export function reasonLine(note: string | undefined, max = 200): string {
  const lines = (note ?? "")
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  const items = lines.filter((l) => /^[-*] /.test(l)).map((l) => l.slice(2));
  const line = plainLine(items.length ? items.join("; ") : (lines[lines.length - 1] ?? ""));
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line;
}

function plainLine(line: string): string {
  return line
    .replace(/^(?:#{1,6}|>)\s*/, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/\*\*(?=\S)(.*?\S)\*\*/g, "$1")
    .replace(/(^|[^\w*])\*(?=\S)([^*]*?\S)\*(?![\w*])/g, "$1$2")
    .replace(/(^|\W)(__?)(?=[^\s_])(.*?[^\s_])\2(?!\w)/g, "$1$3")
    .trim();
}

export interface PriorityLook {
  value: string;
  label: string;
  icon: string;
}

const PRIORITY: Record<string, { key: string; icon: string }> = {
  urgent: { key: "lib.task_detail.priority.urgent", icon: "i-alert" },
  high: { key: "lib.task_detail.priority.high", icon: "i-arrow-up" },
  medium: { key: "lib.task_detail.priority.medium", icon: "i-minus" },
  low: { key: "lib.task_detail.priority.low", icon: "i-arrow-down" },
};

export function priorityLook(value: string | undefined): PriorityLook | null {
  const p = value ? PRIORITY[value] : undefined;
  if (!value || !p) return null;
  return { value, label: t(p.key), icon: p.icon };
}

export function taskPriorityTagHTML(
  value: string | undefined,
  opts: PriorityTagOptions = {},
): string {
  const p = priorityLook(value);
  return p ? priorityTagHTML(p.label, p.value, opts) : "";
}

export interface AttentionInput {
  questions: Array<{
    id: string;
    status: string;
    agent?: string;
    created_at?: string;
    blocking?: boolean;
    blocker?: boolean;
  }>;
  pendingApprovals: Array<{ id: string; label: string; created_at?: string }>;
  changesRequested?: Array<{ id: string; label: string; decided_at?: string }>;
  failingCases: Array<{ key: string; title: string }>;
  unmetHard: Array<{ task_id: string; title?: string }>;
  subtasks?: { total: number; done: number; parentTerminal: boolean; nextLabel: string };
  kids?: Array<{ id: string; title: string; state: string; blocker?: string }>;
}

export type AttentionKind = "question" | "approval" | "test" | "blocked" | "subtasks" | "subtask";

export interface AttentionItem {
  kind: AttentionKind;
  tone: "warning" | "danger" | "success";
  icon: string;
  text: string;
  age: string;
  action: string;
  target: string;
}

const ATTENTION_TONE: Record<AttentionItem["tone"], CalloutTone> = {
  warning: "warn",
  danger: "danger",
  success: "ok",
};

export function attentionRowHTML(i: AttentionItem, extraHTML = ""): string {
  const age = i.age ? ` <span class="attn-age">${esc(i.age)}</span>` : "";
  return calloutHTML({
    tone: ATTENTION_TONE[i.tone],
    band: true,
    icon: i.icon,
    bodyHTML: `<span class="attn-text">${esc(i.text)}</span>${age}`,
    actionsHTML:
      buttonHTML({
        label: i.action,
        size: "sm",
        cls: "attn-btn",
        attrs: { "data-attn": i.kind, "data-target": i.target },
      }) + extraHTML,
    attrs: { "data-attn-row": i.kind },
  });
}

function kidRows(kids: NonNullable<AttentionInput["kids"]>, state: string): AttentionItem[] {
  const hit = kids.filter((k) => k.state === state);
  if (hit.length === 0) return [];
  const first = hit[0];
  const n = hit.length;
  const row = (tone: AttentionItem["tone"], icon: string, text: string): AttentionItem[] => [
    {
      kind: "subtask",
      tone,
      icon,
      text,
      age: "",
      action: t("lib.task_detail.attention.open"),
      target: first.id,
    },
  ];
  if (state === "question")
    return row(
      "warning",
      "i-help",
      n === 1
        ? t("lib.task_detail.attention.kid_question_one", { title: first.title })
        : t("lib.task_detail.attention.kid_question_many", { count: n }),
    );
  if (state === "failed")
    return row(
      "danger",
      "i-x-circle",
      n === 1
        ? t("lib.task_detail.attention.kid_failed_one", { title: first.title })
        : t("lib.task_detail.attention.kid_failed_many", { count: n }),
    );
  return row(
    "warning",
    "i-lock",
    n > 1
      ? t("lib.task_detail.attention.kid_blocked_many", { count: n })
      : first.blocker
        ? t("lib.task_detail.attention.kid_waits", { title: first.title, blocker: first.blocker })
        : t("lib.task_detail.attention.kid_blocked_one", { title: first.title }),
  );
}

export function attentionItems(input: AttentionInput): AttentionItem[] {
  const out: AttentionItem[] = [];
  const open = input.questions.filter((q) => q.status === "open");
  if (open.length > 0) {
    const q = open.find((x) => x.blocker) ?? open.find((x) => x.blocking !== false) ?? open[0];
    const working = q.blocking === false;
    const agent = { agent: q.agent ?? "" };
    let text: string;
    if (q.blocker) {
      text = q.agent
        ? t("lib.task_detail.attention.blocker_by", agent)
        : t("lib.task_detail.attention.blocker");
    } else if (working) {
      text = q.agent
        ? t("lib.task_detail.attention.question_working_by", agent)
        : t("lib.task_detail.attention.question_working");
    } else {
      text = q.agent
        ? t("lib.task_detail.attention.question_by", agent)
        : t("lib.task_detail.attention.question");
    }
    out.push({
      kind: "question",
      tone: "warning",
      icon: "i-help",
      text,
      age: q.created_at ? agoLabel(q.created_at) : "",
      action: t("lib.task_detail.attention.answer"),
      target: q.id,
    });
  }
  const ap = input.pendingApprovals;
  if (ap.length === 1) {
    out.push({
      kind: "approval",
      tone: "warning",
      icon: "i-flag",
      text: t("lib.task_detail.attention.approval_one", { label: ap[0].label }),
      age: ap[0].created_at ? agoLabel(ap[0].created_at) : "",
      action: t("lib.task_detail.attention.review"),
      target: ap[0].id,
    });
  } else if (ap.length > 1) {
    out.push({
      kind: "approval",
      tone: "warning",
      icon: "i-flag",
      text: t("lib.task_detail.attention.approval_many", { count: ap.length }),
      age: "",
      action: t("lib.task_detail.attention.review"),
      target: ap[0].id,
    });
  }
  const cr = input.changesRequested ?? [];
  if (cr.length > 0) {
    out.push({
      kind: "approval",
      tone: "warning",
      icon: "i-flag",
      text:
        cr.length === 1
          ? t("lib.task_detail.attention.changes_one", { label: cr[0].label })
          : t("lib.task_detail.attention.changes_many", { count: cr.length }),
      age: cr[0].decided_at ? agoLabel(cr[0].decided_at) : "",
      action: t("lib.task_detail.attention.review"),
      target: cr[0].id,
    });
  }
  const kids = input.kids ?? [];
  out.push(...kidRows(kids, "question"));
  const fc = input.failingCases;
  if (fc.length > 0) {
    out.push({
      kind: "test",
      tone: "danger",
      icon: "i-x-circle",
      text:
        fc.length === 1
          ? t("lib.task_detail.attention.test_one", { title: fc[0].title })
          : t("lib.task_detail.attention.test_many", { count: fc.length }),
      age: "",
      action: t("lib.task_detail.attention.open_case"),
      target: fc[0].key,
    });
  }
  out.push(...kidRows(kids, "failed"));
  const bl = input.unmetHard;
  if (bl.length > 0) {
    const title = (bl[0].title ?? "") || bl[0].task_id.slice(0, 6);
    out.push({
      kind: "blocked",
      tone: "warning",
      icon: "i-lock",
      text:
        bl.length === 1
          ? t("lib.task_detail.attention.blocked_one", { title })
          : t("lib.task_detail.attention.blocked_many", { title, count: bl.length - 1 }),
      age: "",
      action: t("lib.task_detail.attention.open"),
      target: bl[0].task_id,
    });
  }
  out.push(...kidRows(kids, "blocked"));
  const st = input.subtasks;
  if (st && st.total > 0 && st.done === st.total && !st.parentTerminal && st.nextLabel) {
    out.push({
      kind: "subtasks",
      tone: "success",
      icon: "i-check-circle",
      text: t("lib.task_detail.attention.subtasks_done", { count: st.total }),
      age: "",
      action: t("lib.task_detail.move_to", { stage: st.nextLabel }),
      target: "",
    });
  }
  return out;
}

export type PrimaryKind =
  | "answer"
  | "review"
  | "more_turns"
  | "retry"
  | "review_retry"
  | "relaunch_stage"
  | "credentials"
  | "pick_agent"
  | "move_parent";

export interface PrimaryInput {
  status?: string;
  pendingApprovals: number;
  children: number;
  childrenDone: number;
  terminal: boolean;
  next: string;
  waitCanceled?: boolean;
}

export function primaryAction(input: PrimaryInput): PrimaryKind | null {
  switch (input.status ?? "") {
    case "awaiting_input":
      return "answer";
    case "awaiting_approval":
      if (input.pendingApprovals > 0) return "review";
      break;
    case "out_of_turns":
    case "needs_more_turns":
      return "more_turns";
    case "failed":
    case "not_started":
      return "retry";
    case "blocked":
      return "review_retry";
    case "resume_failed":
      return "relaunch_stage";
    case "credential_blocked":
      return "credentials";
    case "no_agent":
      return "pick_agent";
    case "":
    case "idle":
      if (input.waitCanceled) return "relaunch_stage";
  }
  if (input.children > 0 && input.childrenDone === input.children && !input.terminal && input.next)
    return "move_parent";
  return null;
}

const RUN_EVENTS = new Set([
  "stage_entered",
  "agent_run_started",
  "agent_run_finished",
  "run_queued",
  "run_not_started",
  "run_queue_canceled",
]);

export function waitCanceled(events: Array<{ kind: string; created_at?: string }>): boolean {
  let last: { kind: string; created_at?: string } | null = null;
  for (const e of events) {
    if (!RUN_EVENTS.has(e.kind)) continue;
    if (!last || (e.created_at ?? "") >= (last.created_at ?? "")) last = e;
  }
  return last?.kind === "run_queue_canceled";
}

export function cancelWaitQuestion(
  agent: string,
  stage: string,
): { title: string; message: string; confirmLabel: string; cancelLabel: string } {
  const relaunch = t("tasks.drawer.header.relaunch_stage");
  const who = agent || t("lib.activity.the_agent");
  return {
    title: agent
      ? t("tasks.comments.cancel_wait_title", { agent })
      : t("tasks.comments.cancel_wait_title_unnamed"),
    message: stage
      ? t("tasks.comments.cancel_wait_message", { agent: who, stage, relaunch })
      : t("tasks.comments.cancel_wait_message_nostage", { agent: who, relaunch }),
    confirmLabel: t("lib.activity.run.cancel_wait"),
    cancelLabel: t("tasks.comments.keep_waiting"),
  };
}

export function causeLink(cause: string | undefined): { href: string; label: string } | null {
  switch (cause ?? "") {
    case "llm_credential":
      return { href: "/settings/#llm", label: t("lib.task_detail.cause.ai_engines") };
    case "credential":
      return { href: "/credentials/", label: t("lib.task_detail.cause.credentials") };
    default:
      return null;
  }
}

export function causePicksAgent(cause: string | undefined): boolean {
  return cause === "no_agent" || cause === "agent_missing" || cause === "agent_invalid";
}

export function statusCause(status: string | undefined): string {
  if (status === "credential_blocked") return "credential";
  if (status === "no_agent") return "no_agent";
  return "";
}

export function quotaLink(): { href: string; label: string } {
  return { href: "/infra/#workspaces", label: t("lib.task_detail.cause.quota") };
}

export interface AgentHintInput {
  stage: string;
  assignments: Record<string, string>;
  flow: Array<{ id: string; label: string }>;
  isSubtask: boolean;
  initial: string;
}

export interface AgentHint {
  agent: string;
  line: string;
  replaces: boolean;
}

export function agentHint(input: AgentHintInput): AgentHint {
  const agent = input.assignments[input.stage] || "";
  const idx = input.flow.findIndex((s) => s.id === input.stage);
  const later = idx < 0 ? [] : input.flow.slice(idx + 1);
  const next = later.find((s) => input.assignments[s.id]);
  const here = input.flow[idx]?.label ?? input.stage;
  if (!agent && input.isSubtask && input.stage === input.initial && next) {
    return {
      agent,
      line: t("lib.task_detail.agent.none_in", {
        stage: here,
        agent: input.assignments[next.id],
        next: next.label,
      }),
      replaces: true,
    };
  }
  if (!next) return { agent, line: "", replaces: false };
  return {
    agent,
    line: t("lib.task_detail.agent.next", { agent: input.assignments[next.id], stage: next.label }),
    replaces: false,
  };
}

export interface SiblingNav {
  index: number;
  total: number;
  prev: string;
  next: string;
}

export function siblingNav(order: string[], id: string): SiblingNav | null {
  const i = order.indexOf(id);
  if (i < 0) return null;
  return {
    index: i + 1,
    total: order.length,
    prev: i > 0 ? order[i - 1] : "",
    next: i < order.length - 1 ? order[i + 1] : "",
  };
}

export type ClickKeys = Pick<MouseEvent, "button" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey">;

export function boardCrumbAction(
  click: ClickKeys,
  board: string,
  current: string,
): "browser" | "close" | "switch" {
  if (click.button !== 0 || click.metaKey || click.ctrlKey || click.shiftKey || click.altKey)
    return "browser";
  return board === current ? "close" : "switch";
}

export function foldIndex(wide: boolean[], rows = 4, cols = 2): number {
  let row = 0;
  let col = 0;
  for (let i = 0; i < wide.length; i++) {
    if (wide[i] && col > 0) {
      row++;
      col = 0;
    }
    if (row >= rows) return i;
    if (wide[i]) {
      row++;
      col = 0;
    } else {
      col++;
      if (col === cols) {
        row++;
        col = 0;
      }
    }
  }
  return wide.length;
}

export function approvalsTabName(pending: number): string {
  return pending > 0
    ? t("lib.task_detail.tabs.approvals_pending", { count: pending })
    : t("lib.task_detail.tabs.approvals");
}

export function reportsTabName(count: number): string {
  return count > 0
    ? t("lib.task_detail.tabs.reports_count", { count })
    : t("lib.task_detail.tabs.reports");
}

export function runsLine(
  runs: number,
  tokens: number,
  cost = 0,
  estimated = 0,
  unpriced = 0,
): string {
  if (!runs) return "";
  const head = {
    runs: t("lib.task_detail.runs", { count: runs }),
    tokens: compactTokens(tokens),
  };
  if (!(cost >= 0.005) && !(unpriced > 0)) return t("lib.task_detail.runs_line", head);
  return t("lib.task_detail.runs_line_cost", {
    ...head,
    cost: estimatedCost(cost >= 0.005 ? cost : 0, estimated, fmtUsd, unpriced).text,
  });
}

export function runsTitle(cost = 0, estimated = 0, unpriced = 0): string {
  return estimatedCost(cost >= 0.005 ? cost : 0, estimated, fmtUsd, unpriced).title;
}

function compactTokens(n: number): string {
  if (n >= 1e6) return t("lib.task_detail.tokens_m", { n: fmtDecimal(n / 1e6, 1) });
  if (n >= 1e3) return t("lib.task_detail.tokens_k", { n: fmtDecimal(n / 1e3, 1) });
  return t("lib.task_detail.tokens", { count: n });
}

export interface OrphanAgent {
  container_id: string;
  name: string;
  agent?: string;
  stage?: string;
  started_at?: string;
}

export function orphanAgentsHTML(orphans: OrphanAgent[] | undefined): string {
  if (!orphans?.length) return "";
  return orphans
    .map((o) => {
      const agent = o.agent ?? o.name;
      const line = o.started_at
        ? t("tasks.drawer.props.orphan_line", { agent, time: agoLabel(o.started_at) })
        : t("tasks.drawer.props.orphan_line_untimed", { agent });
      return (
        `<span class="sbx-line">${badgeHTML(t("tasks.drawer.props.orphan_badge"), "warn", {
          title: t("tasks.drawer.props.orphan_hint"),
        })}<span class="prop-line">${esc(line)}</span>` +
        `<button${attrsHTML({
          class: "link-btn sbx-kill write-only",
          type: "button",
          "data-orphan-remove": o.container_id,
          "aria-label": t("tasks.drawer.props.orphan_remove_named", { name: o.name }),
        })}>${esc(t("tasks.drawer.props.orphan_remove"))}</button></span>`
      );
    })
    .join("");
}

export function orphanSummary(orphans: OrphanAgent[] | undefined): string {
  const count = orphans?.length ?? 0;
  return count ? t("tasks.drawer.props.summary_orphans", { count }) : "";
}
