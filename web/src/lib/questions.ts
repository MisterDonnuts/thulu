import { t } from "../i18n";
import { personName } from "./person";
import { fmtDate } from "./time";
import { agentChipHTML } from "./chips";
import {
  badgeHTML,
  buttonHTML,
  checkHTML,
  esc,
  fieldErrorHTML,
  tagHTML,
  type BadgeTone,
} from "./ui";

export interface QuestionOption {
  label: string;
  description?: string;
}

export interface QuestionItem {
  header?: string;
  question: string;
  options?: QuestionOption[];
  multi_select?: boolean;
  // Absent means true: the task_ask MCP tool defaults it on.
  allow_other?: boolean;
}

export interface QuestionPayload {
  kind?: string;
  blocking?: boolean;
  context?: string;
  questions?: QuestionItem[];
  default?: string;
  needs_credential?: boolean;
}

export interface QuestionAnswer {
  question_index: number;
  selected?: string[];
  other?: string;
}

export interface AnswerBody {
  answers: QuestionAnswer[];
  note?: string;
}

export interface TaskQuestion {
  id: string;
  task_id?: string;
  comment_id: string;
  agent?: string;
  stage?: string;
  kind: string;
  blocking?: boolean;
  payload?: QuestionPayload | null;
  status: string;
  // The daemon may store either the answers array or the whole AnswerBody; recordedAnswer reads
  // both.
  answers?: QuestionAnswer[] | AnswerBody | null;
  note?: string;
  answered_by?: string;
  answered_at?: string;
  delivery?: string;
  created_at?: string;
}

const DELIVERY_KEY: Record<string, string> = {
  steered: "lib.questions.delivery.steered",
  resumed: "lib.questions.delivery.resumed",
  thread: "lib.questions.delivery.thread",
};

export function deliveryText(
  q: Pick<TaskQuestion, "status" | "delivery">,
  resumeFailed = false,
): string {
  if (q.status !== "answered") return "";
  const d = q.delivery ?? "";
  if (d in DELIVERY_KEY) return t(DELIVERY_KEY[d]);
  return resumeFailed
    ? t("lib.questions.delivery.resume_failed")
    : t("lib.questions.delivery.pending");
}

export interface QuestionDraft {
  selected: string[][];
  other: string[];
  note: string;
}

export function questionsByComment(qs: TaskQuestion[] | undefined): Map<string, TaskQuestion> {
  const out = new Map<string, TaskQuestion>();
  for (const q of qs ?? []) if (q.comment_id) out.set(q.comment_id, q);
  return out;
}

export function questionItems(q: TaskQuestion): QuestionItem[] {
  return q.payload?.questions ?? [];
}

export function isBlocker(q: TaskQuestion): boolean {
  return q.kind === "blocker" || q.payload?.kind === "blocker";
}

export function isBlocking(q: TaskQuestion): boolean {
  return (q.blocking ?? q.payload?.blocking) !== false;
}

export function allowsOther(item: QuestionItem): boolean {
  return (item.options ?? []).length === 0 || item.allow_other !== false;
}

export function emptyDraft(q: TaskQuestion): QuestionDraft {
  const n = questionItems(q).length;
  return {
    selected: Array.from({ length: n }, () => []),
    other: Array.from({ length: n }, () => ""),
    note: "",
  };
}

export function toggleOption(
  draft: QuestionDraft,
  index: number,
  label: string,
  multi: boolean,
): QuestionDraft {
  const selected = draft.selected.map((s) => [...s]);
  const cur = selected[index] ?? [];
  if (!multi) selected[index] = [label];
  else selected[index] = cur.includes(label) ? cur.filter((l) => l !== label) : [...cur, label];
  return { ...draft, selected };
}

export type AnswerProblem = { reason: "unanswered"; index: number } | { reason: "blocker_text" };

export function answerProblem(q: TaskQuestion, draft: QuestionDraft): AnswerProblem | null {
  const items = questionItems(q);
  for (let i = 0; i < items.length; i++) {
    const picked = (draft.selected[i] ?? []).length > 0;
    const wrote = allowsOther(items[i]) && (draft.other[i] ?? "").trim() !== "";
    if (!picked && !wrote) return { reason: "unanswered", index: i };
  }
  if (isBlocker(q)) {
    const anyOther = items.some((it, i) => allowsOther(it) && (draft.other[i] ?? "").trim());
    if (!anyOther && !draft.note.trim()) return { reason: "blocker_text" };
  }
  return null;
}

export function answerProblemMessage(q: TaskQuestion, p: AnswerProblem): string {
  if (p.reason === "blocker_text") return t("lib.questions.errors.blocker_text");
  const items = questionItems(q);
  const item = p.index < items.length ? items[p.index] : undefined;
  const header = item?.header ?? "";
  const name = header !== "" ? header : String(p.index + 1);
  if (item && (item.options ?? []).length === 0)
    return t("lib.questions.errors.unanswered_text", { question: name });
  return t("lib.questions.errors.unanswered", { question: name });
}

export function answerBody(q: TaskQuestion, draft: QuestionDraft): AnswerBody {
  const answers = questionItems(q).map((item, i) => {
    const offered = new Set((item.options ?? []).map((o) => o.label));
    const a: QuestionAnswer = {
      question_index: i,
      selected: (draft.selected[i] ?? []).filter((l) => offered.has(l)),
    };
    const other = allowsOther(item) ? (draft.other[i] ?? "").trim() : "";
    if (other) a.other = other;
    return a;
  });
  const note = draft.note.trim();
  return note ? { answers, note } : { answers };
}

export function recordedAnswer(q: TaskQuestion): AnswerBody {
  const a = q.answers;
  if (Array.isArray(a)) return { answers: a, note: q.note ?? "" };
  if (a && Array.isArray(a.answers)) return { answers: a.answers, note: a.note ?? q.note ?? "" };
  return { answers: [], note: q.note ?? "" };
}

export function questionSig(q: TaskQuestion): string {
  return `${q.id}:${q.status}:${q.answered_at ?? ""}:${q.delivery ?? ""}`;
}

const STATUS_KEY: Record<string, string> = {
  open: "lib.questions.status.open",
  answered: "lib.questions.status.answered",
  canceled: "lib.questions.status.canceled",
};

function statusLabel(status: string): string {
  return status in STATUS_KEY ? t(STATUS_KEY[status]) : status;
}

const STATUS_TONE: Record<string, BadgeTone> = {
  open: "warn",
  answered: "ok",
  canceled: "muted",
};

export function questionStatusBadgeHTML(status: string): string {
  return badgeHTML(statusLabel(status), STATUS_TONE[status] ?? "neutral", {
    size: "sm",
    cls: "q-state",
    attrs: { "data-status": status },
  });
}

export function questionHTML(
  q: TaskQuestion,
  draft: QuestionDraft,
  opts: { pinned?: boolean; resumeFailed?: boolean } = {},
): string {
  const open = q.status === "open";
  const blocker = isBlocker(q);
  const items = questionItems(q);
  const recorded = open ? null : recordedAnswer(q);
  const id = esc(q.id);

  const kind = blocker ? t("lib.questions.kind.blocker") : t("lib.questions.kind.question");
  const chips = [
    `<span class="q-kind">${esc(kind)}</span>`,
    q.agent && !opts.pinned ? agentChipHTML(q.agent, { inline: true, size: "sm" }) : "",
    isBlocking(q) ? "" : tagHTML(t("lib.questions.non_blocking"), { size: "sm" }),
    opts.pinned ? "" : questionStatusBadgeHTML(q.status),
  ].join("");
  const context = q.payload?.context
    ? `<div class="q-context">${esc(q.payload.context)}</div>`
    : "";

  const itemHTML = (item: QuestionItem, i: number) => {
    const multi = !!item.multi_select;
    const given = recorded?.answers.find((a) => a.question_index === i);
    const picked = open ? (draft.selected[i] ?? []) : (given?.selected ?? []);
    const opts = (item.options ?? [])
      .map((o) =>
        checkHTML({
          type: multi ? "checkbox" : "radio",
          card: open,
          name: `q-${id}-${i}`,
          value: o.label,
          label: o.label,
          ...(o.description ? { hint: o.description } : {}),
          checked: picked.includes(o.label),
          disabled: !open,
          cls: "q-opt",
          inputAttrs: { "data-q-opt": String(i) },
        }),
      )
      .join("");
    let other = "";
    if (open && allowsOther(item)) {
      const ph =
        (item.options ?? []).length === 0
          ? t("lib.questions.answer_placeholder")
          : t("lib.questions.other_placeholder");
      other = `<input class="q-other" type="text" data-q-other="${i}" placeholder="${esc(ph)}"
               value="${esc(draft.other[i] ?? "")}" />`;
    } else if (!open && given?.other) {
      other = `<div class="q-answer-other"><span class="q-answer-k">${esc(t("lib.questions.other"))}</span> ${esc(given.other)}</div>`;
    }
    const header = item.header ? tagHTML(item.header, { size: "sm" }) : "";
    const hint =
      open && multi ? `<span class="q-hint">${esc(t("lib.questions.pick_many"))}</span>` : "";
    return `<fieldset class="q-item">
        <legend class="q-question">${header}<span>${esc(item.question)}</span>${hint}</legend>
        ${opts ? `<div class="q-opts">${opts}</div>` : ""}${other}
      </fieldset>`;
  };

  const fallback =
    !isBlocking(q) && q.payload?.default
      ? `<div class="q-default"><span class="q-answer-k">${esc(t("lib.questions.default_label"))}</span> ${esc(q.payload.default)}</div>`
      : "";

  let foot: string;
  if (open) {
    const notePH = blocker
      ? t("lib.questions.note_placeholder_blocker")
      : t("lib.questions.note_placeholder");
    // The thread is visible to everyone on the task, so needs_credential points secrets to
    // Credentials.
    const secret = q.payload?.needs_credential
      ? `<p class="q-secret">${esc(t("lib.questions.secret_warning"))} <a href="/credentials/">${esc(t("lib.questions.secret_link"))}</a></p>`
      : "";
    foot = `${secret}<textarea class="q-note" data-q-note rows="2" placeholder="${esc(notePH)}">${esc(draft.note)}</textarea>
      ${fieldErrorHTML({ cls: "q-error" })}
      <div class="q-actions">
        ${buttonHTML({ label: t("lib.questions.submit"), size: "sm", variant: "primary", attrs: { "data-q-submit": q.id } })}
        ${buttonHTML({ label: t("lib.questions.cancel"), size: "sm", variant: "ghost", cls: "q-cancel", attrs: { "data-q-cancel": q.id } })}
      </div>`;
  } else {
    const note = recorded?.note
      ? `<div class="q-answer-note"><span class="q-answer-k">${esc(t("lib.questions.note"))}</span> ${esc(recorded.note)}</div>`
      : "";
    let who = "";
    if (q.status === "answered") {
      who = q.answered_by
        ? t("lib.questions.answered_by", {
            who: personName(q.answered_by),
            when: fmtDate(q.answered_at ?? ""),
          })
        : t("lib.questions.answered_at", { when: fmtDate(q.answered_at ?? "") });
    } else if (q.status === "canceled") {
      who = q.answered_by
        ? t("lib.questions.canceled_by", { who: personName(q.answered_by) })
        : t("lib.questions.canceled");
    }
    const went = deliveryText(q, opts.resumeFailed);
    foot = `${note}${who ? `<div class="q-who">${esc(who)}</div>` : ""}${
      went
        ? `<div class="q-delivery" data-delivery="${esc(q.delivery ?? "")}">${esc(went)}</div>`
        : ""
    }`;
  }

  return `<div class="q-card${blocker ? " q-blocker" : ""} q-${esc(q.status)}" data-question="${id}">
      <div class="q-head">${chips}</div>
      ${context}
      ${items.map(itemHTML).join("")}
      ${fallback}
      ${foot}
    </div>`;
}
