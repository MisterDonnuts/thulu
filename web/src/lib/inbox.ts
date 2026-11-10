import { t } from "../i18n";
import type { Approval } from "./approvals";
import { isBlocker, isBlocking, questionItems, type TaskQuestion } from "./questions";
import { agoLabel } from "./task-card";
import { agentChipHTML } from "./chips";
import { stagePillHTML } from "./stages";
import { runPillHTML, runWord } from "./task-detail";
import { approvalsAnchor, taskAnchor } from "./test-cases";
import { badgeHTML, buttonHTML, esc, tableRowHTML, type TableColumn, type TableRow } from "./ui";

interface TaskRef {
  task_id: string;
  task_title?: string;
  stage?: string;
  task_stage?: string;
  board_id?: string;
}

export type InboxQuestion = TaskQuestion & TaskRef;
export type InboxApproval = Approval & TaskRef;

export interface InboxStalled extends TaskRef {
  status: string;
  updated_at: string;
}

export interface InboxResponse {
  questions?: InboxQuestion[] | null;
  approvals?: InboxApproval[] | null;
  stalled?: InboxStalled[] | null;
}

export function inboxTotal(resp: InboxResponse): number {
  const tasks = new Set<string>();
  for (const rows of [resp.questions, resp.approvals, resp.stalled]) {
    for (const row of rows ?? []) tasks.add(row.task_id);
  }
  return tasks.size;
}

export function inboxBadgeText(n: number): string {
  return n > 99 ? "99+" : String(n);
}

export function inboxColumns(): TableColumn[] {
  return [
    { label: t("inbox.columns.task"), width: "title" },
    { label: t("inbox.columns.what") },
    { label: t("inbox.columns.stage"), width: "shrink" },
    { label: t("inbox.columns.waiting"), width: "shrink", cls: "inbox-when" },
  ];
}

// Must equal inboxColumns().length.
export const INBOX_COLUMNS = 4;

export const INBOX_TABLE = { top: true, stack: true } as const;

function rowsHTML(rows: TableRow[]): string {
  const cols = inboxColumns();
  return rows.map((r) => tableRowHTML(cols, r, INBOX_TABLE)).join("");
}

export function sortQuestions(qs: InboxQuestion[]): InboxQuestion[] {
  return [...qs].sort((a, b) => {
    const blk = Number(isBlocker(b)) - Number(isBlocker(a));
    if (blk !== 0) return blk;
    return (a.created_at ?? "").localeCompare(b.created_at ?? "");
  });
}

export function sortApprovals(as: InboxApproval[]): InboxApproval[] {
  const rank = (a: InboxApproval) => (a.status === "changes_requested" ? 0 : 1);
  return [...as].sort(
    (a, b) => rank(a) - rank(b) || (a.created_at ?? "").localeCompare(b.created_at ?? ""),
  );
}

export function questionHeadline(q: TaskQuestion): string {
  const items = questionItems(q);
  if (items.length === 0) return q.payload?.context ?? "";
  const first = items[0].question;
  return items.length > 1
    ? t("lib.inbox.more_questions", { question: first, count: items.length - 1 })
    : first;
}

function taskCell(r: TaskRef, href = taskAnchor(r.task_id, r.board_id ?? "")): string {
  return `<a class="inbox-task" href="${esc(href)}">${esc(r.task_title ?? r.task_id)}</a>`;
}

function stageCell(r: TaskRef, stageLabels: Map<string, string>): string {
  // Approvals use "" for no stage, so `??` alone would never reach task_stage.
  const s = r.stage !== undefined && r.stage !== "" ? r.stage : (r.task_stage ?? "");
  if (s === "") return "";
  return stagePillHTML(s, { label: stageLabels.get(s) ?? s, color: "" }, { size: "sm" });
}

export function questionRowsHTML(qs: InboxQuestion[], stageLabels: Map<string, string>): string {
  return rowsHTML(
    sortQuestions(qs).map((q): TableRow => {
      const blocker = isBlocker(q);
      const kind = blocker
        ? t("lib.inbox.kind.blocker")
        : isBlocking(q)
          ? t("lib.inbox.kind.question")
          : t("lib.inbox.kind.question_working");
      return {
        tone: blocker ? "warn" : undefined,
        cells: [
          { html: taskCell(q) },
          {
            html:
              badgeHTML(kind, blocker ? "warn" : "accent", { size: "sm", cls: "inbox-kind" }) +
              (q.agent ? agentChipHTML(q.agent, { inline: true, size: "sm" }) : "") +
              `<div class="inbox-what">${esc(questionHeadline(q))}</div>`,
          },
          { html: stageCell(q, stageLabels) },
          agoLabel(q.created_at),
        ],
      };
    }),
  );
}

export function approvalRowsHTML(as: InboxApproval[], stageLabels: Map<string, string>): string {
  return rowsHTML(
    sortApprovals(as).map((a): TableRow => {
      const changes = a.status === "changes_requested";
      return {
        tone: changes ? "warn" : undefined,
        cells: [
          { html: taskCell(a, approvalsAnchor(a.task_id, a.board_id ?? "")) },
          {
            html:
              badgeHTML(a.label, "accent", { size: "sm", cls: "inbox-kind" }) +
              (changes ? badgeHTML(t("lib.inbox.changes_requested"), "warn", { size: "sm" }) : "") +
              (a.reason ? `<div class="inbox-what">${esc(a.reason)}</div>` : "") +
              `<div class="inbox-acts">${buttonHTML({ label: t("lib.inbox.approve"), size: "sm", attrs: { "data-approve": a.id, "data-task": a.task_id } })}</div>`,
          },
          { html: stageCell(a, stageLabels) },
          agoLabel(a.created_at),
        ],
      };
    }),
  );
}

export function stalledHint(status: string): string {
  switch (status) {
    case "resume_failed":
      return t("lib.inbox.stalled_hint.resume_failed");
    case "failed":
    case "not_started":
      return t("lib.inbox.stalled_hint.retry");
    case "out_of_turns":
    case "needs_more_turns":
      return t("lib.inbox.stalled_hint.more_turns");
    case "blocked":
      return t("lib.inbox.stalled_hint.blocked");
    case "credential_blocked":
      return t("lib.inbox.stalled_hint.credentials");
    case "no_agent":
      return t("lib.inbox.stalled_hint.no_agent");
    default:
      return "";
  }
}

export function stalledRowsHTML(ss: InboxStalled[], stageLabels: Map<string, string>): string {
  return rowsHTML(
    [...ss]
      .sort((a, b) => a.updated_at.localeCompare(b.updated_at))
      .map((s): TableRow => {
        const hint = stalledHint(s.status);
        return {
          cells: [
            { html: taskCell(s) },
            {
              html:
                (runPillHTML(runWord(s.status, false), "", { cls: "inbox-kind" }) ||
                  badgeHTML(s.status, "neutral", { size: "sm", cls: "inbox-kind" })) +
                (hint ? `<div class="inbox-what">${esc(hint)}</div>` : ""),
            },
            { html: stageCell(s, stageLabels) },
            agoLabel(s.updated_at),
          ],
        };
      }),
  );
}
