// Comments in the task's Overview: what people and agents say to each other, typed by the daemon
// (comment.kind and comment.meta). The flow's own trail (runs, stage moves, system notes) belongs
// in Activity; the only system note that stays here is the pause at the end of a thread.
//
// A comment written before the daemon typed them has no kind: it reads as plain conversation,
// except an agent question (known by its task_ask record) and the turn-limit note (known by its
// catalog key).

import { t } from "../i18n";
import { isNoteLine, type StreamComment } from "./activity";
import { refsText } from "./needs-you";
import { avatarHTML, esc, icoSvg, tagHTML, type TagTone } from "./ui";
import { groupThreads, type Thread } from "./threads";

export type CommentKind = "comment" | "question" | "block" | "decision" | "approval" | "pause";

const KINDS = new Set<string>(["comment", "question", "block", "decision", "approval", "pause"]);

// The catalog key of Thulu's turn-limit note before it had a kind.
export const LEGACY_PAUSE_KEY = "api.tasks.comments.agent_turn_limit";

export interface CommentMeta {
  action?: string;
  from_stage?: string;
  to_stage?: string;
  cases?: string[] | null;
  secrets?: string[] | null;
  report_id?: string;
  run_id?: string;
  approval_id?: string;
  turns?: number;
}

export interface KindedComment extends StreamComment {
  kind?: string;
  meta?: CommentMeta | null;
}

export function commentKind(
  c: KindedComment,
  questionComments: ReadonlySet<string> = new Set(),
): CommentKind {
  const k = c.kind ?? "";
  if (k && k !== "comment" && KINDS.has(k)) return k as CommentKind;
  if (questionComments.has(c.id)) return "question";
  if (c.source === "system" && c.key === LEGACY_PAUSE_KEY) return "pause";
  return "comment";
}

export type CommentFilter = "all" | "questions" | "decisions" | "talk";
export const COMMENT_FILTERS: readonly CommentFilter[] = ["all", "questions", "decisions", "talk"];

export function filterOf(kind: CommentKind): Exclude<CommentFilter, "all"> {
  switch (kind) {
    case "question":
      return "questions";
    case "block":
    case "decision":
    case "approval":
      return "decisions";
    case "comment":
    case "pause":
      return "talk";
  }
}

export function filterLabel(f: CommentFilter): string {
  switch (f) {
    case "all":
      return t("lib.comments.filter.all");
    case "questions":
      return t("lib.comments.filter.questions");
    case "decisions":
      return t("lib.comments.filter.decisions");
    case "talk":
      return t("lib.comments.filter.talk");
  }
}

export function parseCommentFilter(v: unknown): CommentFilter {
  return (COMMENT_FILTERS as readonly unknown[]).includes(v) ? (v as CommentFilter) : "all";
}

export type CommentSort = "new" | "old";

export function parseCommentSort(v: unknown): CommentSort {
  return v === "old" ? "old" : "new";
}

// Kept with the account (PUT /me/preferences/task.comments) and mirrored in this browser. Must match
// taskCommentsPref in internal/api/handle_preferences.go.
export const COMMENTS_PREF = "task.comments";
export const COMMENTS_CACHE = "octopus.task.comments";

export interface CommentsPref {
  sort: CommentSort;
}

export function parseCommentsPref(v: unknown): CommentsPref {
  const sort = v && typeof v === "object" ? (v as { sort?: unknown }).sort : undefined;
  return { sort: parseCommentSort(sort) };
}

export function readCachedCommentsPref(storage: Pick<Storage, "getItem"> | null): CommentsPref {
  try {
    const raw = storage?.getItem(COMMENTS_CACHE);
    return parseCommentsPref(raw ? JSON.parse(raw) : null);
  } catch {
    return parseCommentsPref(null);
  }
}

export function cacheCommentsPref(storage: Pick<Storage, "setItem"> | null, v: CommentsPref): void {
  try {
    storage?.setItem(COMMENTS_CACHE, JSON.stringify(v));
  } catch {
    // deliberate: the account still has it; this browser opens on the daemon's answer
  }
}

export function sortLabel(s: CommentSort): string {
  return s === "old" ? t("lib.comments.sort.old") : t("lib.comments.sort.new");
}

// What Comments keeps of a task's comments. A root stays when a person or an agent wrote it or
// when it opens a typed thread (a stop, an approval, a question). A reply stays under a kept root
// when a person or an agent wrote it, when it is a decision or the pause, or when it is a keyed
// system note about that conversation (an @mention that could not run, for one).
export function conversation<C extends KindedComment>(
  comments: readonly C[],
  questionComments: ReadonlySet<string> = new Set(),
): C[] {
  const roots = new Set<string>();
  for (const c of comments) {
    if (c.parent_id) continue;
    const kind = commentKind(c, questionComments);
    if (c.source !== "system" || kind !== "comment") roots.add(c.id);
  }
  return comments.filter((c) => {
    if (!c.parent_id) return roots.has(c.id);
    if (!roots.has(c.parent_id)) return false;
    const kind = commentKind(c, questionComments);
    return c.source !== "system" || kind !== "comment" || isNoteLine(c);
  });
}

export interface CommentThread<C extends KindedComment> extends Thread<C> {
  id: string;
  kind: CommentKind;
  filter: Exclude<CommentFilter, "all">;
  at: string;
}

export function commentThreads<C extends KindedComment>(
  comments: readonly C[],
  questionComments: ReadonlySet<string> = new Set(),
): Array<CommentThread<C>> {
  return groupThreads(conversation(comments, questionComments)).map((th) => {
    const kind = commentKind(th.root, questionComments);
    return { ...th, id: th.root.id, kind, filter: filterOf(kind), at: th.root.created_at };
  });
}

// GET /tasks/{id}'s "waiting" (internal/task Waiting): the approvals that hold the stage the task
// is in and the questions that stage asked.
export interface TaskWaiting {
  approvals?: readonly string[] | null;
  questions?: readonly string[] | null;
}

export interface PendingInput {
  running?: boolean;
  attention?: { thread_id?: string } | null;
  approvals?: ReadonlyArray<{ id?: string; status?: string; thread_id?: string }> | null;
  waiting?: TaskWaiting | null;
}

// The threads that wait for the person: the open stop or question the daemon's attention names,
// and the thread of each approval that holds the stage now. An approval an earlier stage (or an
// agent's flag no stage gates on) left pending stays in the Approvals tab: the flow went on
// without it. A running task waits on nobody.
export function pendingThreadIDs(p: PendingInput): Set<string> {
  const out = new Set<string>();
  if (p.running) return out;
  const att = p.attention?.thread_id ?? "";
  if (att) out.add(att);
  const holding = new Set(p.waiting?.approvals ?? []);
  for (const a of p.approvals ?? []) {
    if (a.status === "pending" && a.thread_id && a.id && holding.has(a.id)) out.add(a.thread_id);
  }
  return out;
}

// The open questions to pin: the ones the stage the task is in asked. A question an earlier stage
// left open went on with its default.
export function waitingQuestions<Q extends { id: string; status?: string }>(
  questions: readonly Q[] | null | undefined,
  waiting: TaskWaiting | null | undefined,
): Q[] {
  const ids = new Set(waiting?.questions ?? []);
  return (questions ?? []).filter((q) => q.status === "open" && ids.has(q.id));
}

export function countByFilter<C extends KindedComment>(
  threads: ReadonlyArray<CommentThread<C>>,
): Record<CommentFilter, number> {
  const n: Record<CommentFilter, number> = { all: 0, questions: 0, decisions: 0, talk: 0 };
  for (const th of threads) {
    n.all++;
    n[th.filter]++;
  }
  return n;
}

// The list under the composer: the threads that match the filter, without the pinned ones (they
// show above), oldest or newest first by when the thread started.
export function listedThreads<C extends KindedComment>(
  threads: ReadonlyArray<CommentThread<C>>,
  opts: { filter: CommentFilter; sort: CommentSort; pinned?: ReadonlySet<string> },
): Array<CommentThread<C>> {
  const pinned = opts.pinned ?? new Set<string>();
  const out = threads
    .filter((th) => !pinned.has(th.id) && (opts.filter === "all" || th.filter === opts.filter))
    .map((th, i) => ({ th, i }))
    .sort((a, b) => (a.th.at < b.th.at ? -1 : a.th.at > b.th.at ? 1 : a.i - b.i))
    .map((x) => x.th);
  return opts.sort === "new" ? out.reverse() : out;
}

export interface DecisionLabel {
  icon: string;
  text: string;
}

// The tag a decision carries: what the person did, in their words for the action.
export function decisionLabel(
  meta: CommentMeta | null | undefined,
  stageLabel: (id: string) => string,
): DecisionLabel {
  const m = meta ?? {};
  const to = m.to_stage ? stageLabel(m.to_stage) : "";
  const cases = refsText((m.cases ?? []).filter(Boolean));
  switch (m.action ?? "") {
    case "move":
      return {
        icon: "i-arrow-right",
        text: to
          ? t("lib.comments.decision.moved_to", { stage: to })
          : t("lib.comments.decision.moved"),
      };
    case "send_back":
      return {
        icon: "i-corner-up",
        text: to
          ? t("lib.comments.decision.sent_back_to", { stage: to })
          : t("lib.comments.decision.sent_back"),
      };
    case "relaunch": {
      const stage = m.from_stage ? stageLabel(m.from_stage) : to;
      return {
        icon: "i-refresh",
        text: stage
          ? t("lib.comments.decision.reran", { stage })
          : t("lib.comments.decision.reran_plain"),
      };
    }
    case "waive":
      return {
        icon: "i-minus",
        text: cases
          ? t("lib.comments.decision.skipped", { cases })
          : t("lib.comments.decision.skipped_plain"),
      };
    case "access":
      return {
        icon: "i-lock",
        text: cases
          ? t("lib.comments.decision.access", { cases })
          : t("lib.comments.decision.access_plain"),
      };
    case "approve":
      return { icon: "i-check", text: t("lib.comments.decision.approved") };
    case "request_changes":
      return { icon: "i-edit", text: t("lib.comments.decision.changes") };
    case "waive_approval":
      return { icon: "i-minus", text: t("lib.comments.decision.approval_skipped") };
    default:
      return { icon: "i-check", text: t("lib.comments.decision.other") };
  }
}

interface KindTagInput {
  kind: CommentKind;
  meta?: CommentMeta | null;
  pending?: boolean;
  resolved?: boolean;
}

// A comment's kind tag: a small label with an icon, violet for a decision or a question, orange
// for what waits on a person, green for what a person approved, grey for a stop that is over.
export function cmTagHTML(
  label: string,
  icon: string,
  tone: "dec" | "blk" | "ok" | "muted",
): string {
  const paint: Record<typeof tone, TagTone> = {
    dec: "special",
    blk: "warn",
    ok: "neutral",
    muted: "neutral",
  };
  return tagHTML(label, { size: "sm", tone: paint[tone], icon, cls: `cm-tag cm-tag-${tone}` });
}

// The tag beside the author: what kind of comment this is. Plain conversation and the pause carry
// none.
export function kindTagHTML(c: KindTagInput, stageLabel: (id: string) => string): string {
  switch (c.kind) {
    case "block":
      return c.pending
        ? cmTagHTML(t("lib.comments.kind.waiting"), "i-stop", "blk")
        : cmTagHTML(t("lib.comments.kind.stopped"), "i-stop", "muted");
    case "approval":
      return cmTagHTML(
        t("lib.comments.kind.approval"),
        "i-lock",
        c.pending ? "blk" : c.resolved ? "ok" : "muted",
      );
    case "question":
      return cmTagHTML(t("lib.comments.kind.question"), "i-help", "dec");
    case "decision": {
      const d = decisionLabel(c.meta, stageLabel);
      return cmTagHTML(d.text, d.icon, c.meta?.action === "approve" ? "ok" : "dec");
    }
    case "comment":
    case "pause":
      return "";
  }
}

export function pauseText(turns: number | undefined): string {
  return turns && turns > 0
    ? t("lib.comments.paused", { count: turns })
    : t("lib.comments.paused_plain");
}

// The cut a pause note draws inside its thread.
export function pauseHTML(c: KindedComment): string {
  return `<div class="cm-pause" id="comment-${esc(c.id)}" role="note">${icoSvg("i-clock")}<span>${esc(pauseText(c.meta?.turns))}</span></div>`;
}

// Long bodies start folded to a few lines.
export const LONG_BODY_CHARS = 420;
export const LONG_BODY_LINES = 8;

export function isLongBody(body: string | undefined): boolean {
  const b = (body ?? "").trim();
  return b.length > LONG_BODY_CHARS || b.split("\n").length > LONG_BODY_LINES;
}

// The kinds that belong to the flow of a stage visit, so Activity has the same moment to show.
export function hasVisit(kind: CommentKind): boolean {
  return kind === "block" || kind === "decision" || kind === "approval" || kind === "question";
}

export interface MentionCandidate {
  handle: string;
  // Other words a person may type to find the agent: its name, its description, its column.
  words?: ReadonlyArray<string | undefined>;
}

// Agents for "@query", best first: the handle starts with the query, then one of its words does
// ("evi" finds thulu-evidence), then the handle holds it anywhere, then its name, description or
// column does. Ties keep the order the agents came in.
export function rankMentions<A extends MentionCandidate>(
  agents: readonly A[],
  query: string,
  limit = 7,
): A[] {
  const q = query.trim().toLowerCase();
  const rank = (a: A): number => {
    const h = a.handle.toLowerCase();
    if (h.startsWith(q)) return 0;
    if (h.split(/[-_.]/).some((w) => w.startsWith(q))) return 1;
    if (h.includes(q)) return 2;
    if ((a.words ?? []).some((w) => (w ?? "").toLowerCase().includes(q))) return 3;
    return 9;
  };
  return agents
    .map((a, i) => ({ a, i, r: rank(a) }))
    .filter((x) => x.r < 9)
    .sort((x, y) => x.r - y.r || x.i - y.i)
    .slice(0, limit)
    .map((x) => x.a);
}

// The threads pinned under "Waiting for you", oldest first. A question is left out: the open
// questions have their own answer form in that same block.
export function pinnedThreads<C extends KindedComment>(
  threads: ReadonlyArray<CommentThread<C>>,
  pending: ReadonlySet<string>,
): Array<CommentThread<C>> {
  return threads
    .filter((th) => pending.has(th.id) && th.kind !== "question")
    .sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
}

// The two letters on an agent's avatar: its role, without the workspace prefix
// ("thulu-evidence" is EV, "thulu-test-designer" is TE, "reviewer" is RE).
export function agentMark(handle: string): string {
  const parts = handle
    .replace(/^agent:/, "")
    .split("-")
    .filter(Boolean);
  const role = parts.length > 1 ? parts.slice(1).join("-") : (parts[0] ?? "");
  const letters = Array.from(role.replace(/[^\p{L}\p{N}]/gu, ""))
    .slice(0, 2)
    .join("");
  return letters ? letters.toLocaleUpperCase() : "?";
}

export interface ColoredStage {
  id: string;
  color?: string;
}

const HEX_COLOR = /^#[0-9a-f]{3,8}$/i;
export const AGENT_GREY = "#9aa0aa";

// The colour of an agent's avatar: the colour of the column it works on this task, else of the
// column the comment was written in, else grey. Only #hex values come back, so the result can go
// into a style attribute.
export function agentColor(
  handle: string,
  stages: readonly ColoredStage[],
  assignments: Readonly<Record<string, string>> | null | undefined,
  stage?: string,
): string {
  const hex = (id: string | undefined) => {
    const c = stages.find((s) => s.id === id)?.color ?? "";
    return HEX_COLOR.test(c) ? c : "";
  };
  const worked = Object.entries(assignments ?? {}).find(([, a]) => a === handle)?.[0];
  return hex(worked) || hex(stage) || AGENT_GREY;
}

// Marks every @handle of a known agent in rendered HTML as a mention chip. Text inside tags,
// code, pre and links is left alone.
export function highlightMentions(html: string, handles: readonly string[]): string {
  const known = new Set(handles.map((h) => h.toLowerCase()));
  if (known.size === 0) return html;
  let skip = 0;
  return html
    .split(/(<[^>]+>)/)
    .map((part) => {
      const tag = /^<(\/?)(code|pre|a)\b/i.exec(part);
      if (tag) {
        skip = Math.max(0, skip + (tag[1] ? -1 : 1));
        return part;
      }
      if (part.startsWith("<") || skip > 0) return part;
      return part.replace(/(^|[\s(])@([a-z][\w-]*)/gi, (m, pre: string, h: string) =>
        known.has(h.toLowerCase()) ? `${pre}<span class="mention">@${h}</span>` : m,
      );
    })
    .join("");
}

interface AnswerReplyCandidate {
  id: string;
  source?: string;
  created_at: string;
}

// The reply that carries a person's answer to a question: the first reply a person posted within
// a minute of the answer. The question card shows that answer, so the reply does not repeat it.
export function answerReplyID(
  q: { status: string; answered_at?: string },
  replies: readonly AnswerReplyCandidate[],
): string {
  if (q.status !== "answered" || !q.answered_at) return "";
  const at = Date.parse(q.answered_at);
  if (isNaN(at)) return "";
  const hit = replies.find((r) => {
    if (r.source === "agent" || r.source === "auditor" || r.source === "system") return false;
    const when = Date.parse(r.created_at);
    return !isNaN(when) && Math.abs(when - at) <= 60_000;
  });
  return hit?.id ?? "";
}

// A comment's avatar: a person's initials on a lilac disc, an agent's role letters on a square in
// its column's colour, Thulu's on grey.
export function cmAvatarHTML(
  kind: "agent" | "person" | "system",
  name: string,
  color: string = AGENT_GREY,
): string {
  if (kind === "person") return avatarHTML({ kind: "person", name, cls: "cm-av" });
  const bg = HEX_COLOR.test(color) ? color : AGENT_GREY;
  return avatarHTML({
    kind,
    name: kind === "agent" ? agentMark(name) : name,
    letters: true,
    shape: "square",
    cls: "cm-av",
    attrs: { style: `--av-bg:${bg}` },
  });
}
