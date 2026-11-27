// The Activity tab of a task: its journey across the columns and one card per round, each with the
// visits to a column in that round, how each visit ended and what happened in it (agent runs,
// questions, stops and what a person decided). It reads the structured trail the daemon keeps:
// stage_entered with by_person, round and comment_id, comment kinds and meta, relaunch_requested and
// test_cases_access_given. An older task without comment ids still reads: the move says "a person"
// and has no link to a reason.

import { SLOT, t, tParts } from "../i18n";
import {
  authorLabel,
  classifyEvent,
  type RunInfo,
  type StreamEvent,
  type SystemEntry,
  type Viewer,
} from "./activity";
import {
  excerpt,
  runNoticesHTML,
  runPillHTML,
  systemTextHTML,
  type ActivityCtx,
} from "./activity-html";
import { cmAvatarHTML, commentKind, decisionLabel, type KindedComment } from "./comments";
import { spanText } from "./journey";
import type { VerdictMark, Visit } from "./rounds";
import { refsText } from "./needs-you";
import { fmtClock, fmtDayClock, fmtDate } from "./time";
import {
  attrsHTML,
  badgeHTML,
  buttonHTML,
  checkHTML,
  emptyStateHTML,
  esc,
  icoSvg,
  sectionHeadHTML,
} from "./ui";

// Rounds shown at first, and added by each "Show earlier rounds".
export const ROUND_STEP = 3;

export type EndKind =
  | "passed"
  | "agent_back"
  | "person_back"
  | "person_moved"
  | "stopped_moved"
  | "stopped_back"
  | "running"
  | "waiting"
  | "idle";

export interface VisitEnd {
  kind: EndKind;
  to: string;
  // The agent that sent it back, or the person who moved it (raw author; empty when unknown).
  who: string;
  whoSource: string;
  commentID: string;
}

export type ItemKind =
  "run" | "question" | "stop" | "decision" | "relaunch" | "access" | "comment" | "system";

export interface ActivityItem {
  kind: ItemKind;
  at: string;
  run?: RunInfo;
  // A run's last note: the last comment its agent wrote while it ran.
  note?: KindedComment;
  comment?: KindedComment;
  reply?: KindedComment;
  event?: StreamEvent;
}

export interface VisitCard {
  key: string;
  visit: Visit;
  end: VisitEnd;
  items: ActivityItem[];
  runs: RunInfo[];
  questions: number;
  stops: number;
  failed: number;
}

export interface RoundCard {
  n: number;
  at: string;
  // The visit that opened the round, for "why"; the first visit of the task opens round 1.
  opener: Visit;
  // Who opened the round and why: the agent's verdict reason, or the start of a person's comment.
  openedBy: { who: string; whoSource: string; back: boolean; from: string; reason: string };
  visits: VisitCard[];
}

export interface RoundsInput {
  visits: readonly Visit[];
  runs: readonly RunInfo[];
  comments: readonly KindedComment[];
  events: readonly StreamEvent[];
  // The task's state now, for the visit it is still in.
  running: boolean;
  waiting: boolean;
  // The comments an agent question lives in (task_ask records), for comments written before the
  // daemon typed them.
  questionComments?: ReadonlySet<string>;
}

// A visit's key, stable while the task's trail only grows: its round and its place in the trail.
export function visitKey(v: Pick<Visit, "round">, index: number): string {
  return `${v.round}:${index}`;
}

export function inVisit(v: Pick<Visit, "at" | "until">, at: string): boolean {
  return !!at && at >= v.at && (!v.until || at < v.until);
}

const FAILED = new Set(["failed", "not_started", "out_of_turns"]);

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

// The comments a visit's end points at and the relaunches' reasons: they show in their own line,
// not again as a decision.
function referencedComments(input: RoundsInput): Set<string> {
  const ids = new Set<string>();
  for (const v of input.visits) {
    if (v.commentID) ids.add(v.commentID);
    if (v.next?.commentID) ids.add(v.next.commentID);
  }
  for (const e of input.events) {
    const id = str(e.payload?.comment_id);
    if (id) ids.add(id);
  }
  return ids;
}

export function visitEnd(
  v: Visit,
  isLast: boolean,
  input: Pick<RoundsInput, "comments" | "running" | "waiting">,
  runs: readonly RunInfo[],
  stopped: boolean,
): VisitEnd {
  const none = { who: "", whoSource: "", commentID: "" };
  if (!v.next) {
    if (isLast && input.running) return { kind: "running", to: "", ...none };
    if (isLast && input.waiting) return { kind: "waiting", to: "", ...none };
    return { kind: "idle", to: "", ...none };
  }
  const n = v.next;
  const commentID = n.commentID ?? "";
  if (!n.byPerson) {
    if (!n.back) return { kind: "passed", to: n.stage, ...none };
    const agent = [runs.at(-1)?.agent, v.verdicts.at(-1)?.agent].find(Boolean) ?? "";
    return { kind: "agent_back", to: n.stage, who: agent, whoSource: "agent", commentID: "" };
  }
  const c = commentID ? input.comments.find((x) => x.id === commentID) : undefined;
  const who = c?.author ?? "";
  const whoSource = c?.source ?? "user";
  const kind: EndKind = n.back
    ? stopped
      ? "stopped_back"
      : "person_back"
    : stopped
      ? "stopped_moved"
      : "person_moved";
  return { kind, to: n.stage, who, whoSource, commentID };
}

function visitItems(
  v: Visit,
  input: RoundsInput,
  referenced: ReadonlySet<string>,
  system: boolean,
): ActivityItem[] {
  const items: ActivityItem[] = [];
  for (const r of input.runs) {
    if (inVisit(v, r.startedAt))
      items.push({ kind: "run", at: r.startedAt, run: r, note: runNote(r, input) });
  }
  const replies = (id: string) => input.comments.filter((c) => c.parent_id === id);
  for (const c of input.comments) {
    if (c.parent_id || !inVisit(v, c.created_at)) continue;
    const kind = commentKind(c, input.questionComments);
    if (kind === "question")
      items.push({ kind: "question", at: c.created_at, comment: c, reply: replies(c.id)[0] });
    else if (kind === "block") {
      const reply = replies(c.id).find((r) => commentKind(r) === "decision") ?? replies(c.id)[0];
      items.push({ kind: "stop", at: c.created_at, comment: c, reply });
    } else if (kind === "decision" && !referenced.has(c.id)) {
      items.push({ kind: "decision", at: c.created_at, comment: c });
    } else if (kind === "comment" && c.source === "user") {
      items.push({ kind: "comment", at: c.created_at, comment: c });
    } else if (system && (c.source === "system" || kind === "pause")) {
      items.push({ kind: "system", at: c.created_at, comment: c });
    }
  }
  const stopped = items.filter((x) => x.kind === "stop").map((x) => Date.parse(x.at));
  for (const e of input.events) {
    if (!inVisit(v, e.created_at)) continue;
    // A stop the daemon recorded with no typed comment for it (an older task): the event stands in.
    if (e.kind === "task_blocked") {
      const at = Date.parse(e.created_at);
      if (!stopped.some((x) => Math.abs(x - at) <= NEAR_MS))
        items.push({ kind: "stop", at: e.created_at, event: e });
    } else if (e.kind === "relaunch_requested")
      items.push({ kind: "relaunch", at: e.created_at, event: e });
    else if (e.kind === "test_cases_access_given")
      items.push({ kind: "access", at: e.created_at, event: e });
    else if (system && e.kind !== "stage_entered" && classifyEvent(e.kind)) {
      items.push({ kind: "system", at: e.created_at, event: e });
    }
  }
  return items.sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
}

const NEAR_MS = 5 * 60 * 1000;

// The last comment a run's agent wrote while it ran (not a question or a stop, which have their
// own line).
function runNote(r: RunInfo, input: RoundsInput): KindedComment | undefined {
  if (!r.agent || !r.startedAt) return undefined;
  const end = r.finishedAt ? Date.parse(r.finishedAt) + NEAR_MS : Infinity;
  return input.comments
    .filter((c) => {
      if (c.parent_id || c.author !== r.agent || c.source === "user") return false;
      if (c.created_at < r.startedAt || Date.parse(c.created_at) > end) return false;
      const kind = commentKind(c, input.questionComments);
      return kind !== "question" && kind !== "block";
    })
    .at(-1);
}

// The verdict that sent a visit on: its last one, the kick-back when there is one.
function backVerdict(v: Visit): VerdictMark | undefined {
  return [...v.verdicts].reverse().find((x) => x.kickBackTo) ?? v.verdicts.at(-1);
}

// What a verdict says in words: its reason when it is a sentence, else its headline, else the
// reason code.
export function verdictText(v: Pick<VerdictMark, "reason" | "headline"> | undefined): string {
  const reason = (v?.reason ?? "").trim();
  const headline = (v?.headline ?? "").trim();
  return /\s/.test(reason) ? reason : headline || reason;
}

export function buildRounds(input: RoundsInput, opts: { system: boolean }): RoundCard[] {
  const referenced = referencedComments(input);
  const cards = new Map<number, RoundCard>();
  input.visits.forEach((v, i) => {
    const items = visitItems(v, input, referenced, opts.system);
    const runs = items.flatMap((x) => (x.kind === "run" && x.run ? [x.run] : []));
    const stops = items.filter((x) => x.kind === "stop").length;
    const card: VisitCard = {
      key: visitKey(v, i),
      visit: v,
      end: visitEnd(v, i === input.visits.length - 1, input, runs, stops > 0),
      items,
      runs,
      questions: items.filter((x) => x.kind === "question").length,
      stops,
      failed: runs.filter((r) => FAILED.has(r.state)).length,
    };
    let round = cards.get(v.round);
    if (!round) {
      const prev = i > 0 ? input.visits[i - 1] : undefined;
      const c = v.commentID ? input.comments.find((x) => x.id === v.commentID) : undefined;
      const verdict = prev ? backVerdict(prev) : undefined;
      const agent = prev
        ? (input.runs.filter((r) => inVisit(prev, r.startedAt)).at(-1)?.agent ??
          verdict?.agent ??
          "")
        : "";
      const reason = v.byPerson ? excerpt(c?.body ?? "", 240) : verdictText(verdict);
      round = {
        n: v.round,
        at: v.at,
        opener: v,
        openedBy: {
          who: v.byPerson ? (c?.author ?? "") : agent,
          whoSource: v.byPerson ? (c?.source ?? "user") : "agent",
          back: !!prev?.next?.back,
          from: v.from,
          reason,
        },
        visits: [],
      };
      cards.set(v.round, round);
    }
    round.visits.push(card);
  });
  return [...cards.values()].sort((a, b) => a.n - b.n);
}

export interface RoundFilter {
  stage: string;
  agent: string;
}

// A round stays when any of its visits matches; the visits that do not match leave it.
export function filterRounds(rounds: readonly RoundCard[], f: RoundFilter): RoundCard[] {
  const match = (v: VisitCard) =>
    (!f.stage || v.visit.stage === f.stage) &&
    (!f.agent || v.runs.some((r) => r.agent === f.agent));
  return rounds
    .map((r) => ({ ...r, visits: r.visits.filter(match) }))
    .filter((r) => r.visits.length);
}

// The visit a comment belongs to, for "See in Activity": the visit whose end it explains (a
// person's reason for moving the task on), else the one it opened, else the one it was written in.
export function visitForComment(
  visits: readonly Visit[],
  c: { id: string; at: string },
): { key: string; round: number } | null {
  const pick = (i: number) =>
    i >= 0 ? { key: visitKey(visits[i], i), round: visits[i].round } : null;
  if (c.id) {
    const ended = visits.findIndex((v) => v.next?.commentID === c.id);
    if (ended >= 0) return pick(ended);
    const opened = visits.findIndex((v) => v.commentID === c.id);
    if (opened >= 0) return pick(opened);
  }
  return pick(visits.findIndex((v) => inVisit(v, c.at)));
}

// How many rounds to show so that `round` is among them: at least `shown`, in whole pages.
export function shownToInclude(
  visits: ReadonlyArray<Pick<Visit, "round">>,
  round: number,
  shown: number,
): number {
  const nums = [...new Set(visits.map((v) => v.round))].sort((a, b) => a - b);
  const at = nums.indexOf(round);
  if (at < 0) return shown;
  const need = nums.length - at;
  return Math.max(shown, Math.ceil(need / ROUND_STEP) * ROUND_STEP);
}

// The newest `shown` rounds, in the order asked, and how many earlier ones are left out.
export function pageRounds(
  rounds: readonly RoundCard[],
  shown: number,
  newestFirst: boolean,
): { cards: RoundCard[]; hidden: number } {
  const cards = rounds.slice(-Math.max(shown, 0));
  return {
    cards: newestFirst ? [...cards].reverse() : cards,
    hidden: rounds.length - cards.length,
  };
}

// ---------- HTML ----------

export interface RoundsView {
  ctx: ActivityCtx;
  viewer?: Viewer | null;
  stageLabel: (id: string) => string;
  open: ReadonlySet<string>;
  // An agent's avatar colour: the colour of the column it works on.
  agentColor?: (agent: string, stage: string) => string;
  // A comment body as sanitized markdown HTML, for a run's last note.
  noteHTML?: (body: string) => string;
}

function personName(who: string, source: string, viewer?: Viewer | null): string {
  if (!who) return t("lib.activity_rounds.a_person");
  return authorLabel({ author: who, source }, viewer);
}

function agentName(who: string): string {
  return who || t("lib.activity.the_agent");
}

// A catalog line with its actor in bold: tParts splits it at the slot.
function boldHTML(parts: readonly string[], who: string): string {
  return parts.map((p, i) => (i % 2 ? `<b>${esc(who)}</b>` : esc(p))).join("");
}

function plainParts(parts: readonly string[], who: string): string {
  return parts.map((p, i) => (i % 2 ? who : p)).join("");
}

export function visitEndText(
  end: VisitEnd,
  view: Pick<RoundsView, "stageLabel" | "viewer">,
): string {
  const to = end.to ? view.stageLabel(end.to) : "";
  const who = personName(end.who, end.whoSource, view.viewer);
  switch (end.kind) {
    case "passed":
      return t("lib.activity_rounds.end.passed", { stage: to });
    case "agent_back":
      return t("lib.activity_rounds.end.agent_back", { agent: agentName(end.who), stage: to });
    case "person_back":
      return t("lib.activity_rounds.end.person_back", { who, stage: to });
    case "person_moved":
      return t("lib.activity_rounds.end.person_moved", { who, stage: to });
    case "stopped_back":
      return t("lib.activity_rounds.end.stopped_back", { who, stage: to });
    case "stopped_moved":
      return t("lib.activity_rounds.end.stopped_moved", { who, stage: to });
    case "running":
      return t("lib.activity_rounds.end.running");
    case "waiting":
      return t("lib.activity_rounds.end.waiting");
    case "idle":
      return t("lib.activity_rounds.end.idle");
  }
}

// How a visit ended, as the Task View Lab's outcome icon: a check when it passed on, a return
// arrow in warn when an agent sent it back, violet when a person moved it.
const END_LOOK: Record<EndKind, { icon: string; st: string }> = {
  passed: { icon: "check", st: "pass" },
  agent_back: { icon: "back", st: "blocked" },
  person_back: { icon: "back", st: "violet" },
  person_moved: { icon: "user", st: "violet" },
  stopped_back: { icon: "back", st: "violet" },
  stopped_moved: { icon: "user", st: "violet" },
  running: { icon: "play", st: "run" },
  waiting: { icon: "user", st: "blocked" },
  idle: { icon: "circle", st: "waived" },
};

// The agent that opened a round, with the column it sent the task back from: "Review (reviewer)".
function roundAgent(r: RoundCard, view: Pick<RoundsView, "stageLabel">): string {
  const who = agentName(r.openedBy.who);
  return r.openedBy.who && r.openedBy.from
    ? t("lib.activity_rounds.why.agent_who", {
        stage: view.stageLabel(r.openedBy.from),
        agent: who,
      })
    : who;
}

// Why a round started: who moved the task and the reason they gave, the actor in bold for HTML.
function roundWhy(
  r: RoundCard,
  view: Pick<RoundsView, "stageLabel" | "viewer">,
  html: boolean,
): string {
  const stage = view.stageLabel(r.opener.stage);
  const reason = r.openedBy.reason;
  const out = (parts: string[], who: string) =>
    html ? boldHTML(parts, who) : plainParts(parts, who);
  const person = () => personName(r.openedBy.who, r.openedBy.whoSource, view.viewer);
  if (r.n === 1 || !r.openedBy.back) {
    if (r.opener.byPerson && r.openedBy.from) {
      return out(
        reason
          ? tParts("lib.activity_rounds.why.moved_reason", { who: SLOT, stage, reason })
          : tParts("lib.activity_rounds.why.moved", { who: SLOT, stage }),
        person(),
      );
    }
    const started = t("lib.activity_rounds.why.started", { stage });
    return html ? esc(started) : started;
  }
  if (r.opener.byPerson) {
    return out(
      reason
        ? tParts("lib.activity_rounds.why.person_back_reason", { who: SLOT, stage, reason })
        : tParts("lib.activity_rounds.why.person_back", { who: SLOT, stage }),
      person(),
    );
  }
  return out(
    reason
      ? tParts("lib.activity_rounds.why.agent_back_reason", { agent: SLOT, stage, reason })
      : tParts("lib.activity_rounds.why.agent_back", { agent: SLOT, stage }),
    roundAgent(r, view),
  );
}

export function roundWhyText(
  r: RoundCard,
  view: Pick<RoundsView, "stageLabel" | "viewer">,
): string {
  return roundWhy(r, view, false);
}

// "who · reason" for each round a return opened, for the journey's tooltips.
export function roundWhys(
  rounds: readonly RoundCard[],
  view: Pick<RoundsView, "stageLabel" | "viewer">,
): Map<number, string> {
  const out = new Map<number, string>();
  for (const r of rounds) {
    if (r.n === 1 || !r.openedBy.back) continue;
    const who = r.opener.byPerson
      ? personName(r.openedBy.who, r.openedBy.whoSource, view.viewer)
      : roundAgent(r, view);
    out.set(r.n, r.openedBy.reason ? `${who} · ${r.openedBy.reason}` : who);
  }
  return out;
}

function linkHTML(label: string, attrs: Record<string, string | number | boolean>): string {
  return buttonHTML({ label, variant: "link", attrs });
}

function reasonLinkHTML(id: string, label = t("lib.activity_rounds.reason")): string {
  return id ? linkHTML(label, { "data-reveal-comment": id }) : "";
}

function clockHTML(at: string): string {
  return at ? `<span class="t" title="${esc(fmtDate(at))}">${esc(fmtClock(at))}</span>` : "";
}

// One line inside a visit: an icon chip, the text, the time and a link to the thread.
function evHTML(tone: string, icon: string, textHTML: string, at: string, link = "", cls = "") {
  return `<div class="ar-ev${cls ? ` ${cls}` : ""}"><span class="ar-bdg-i ${tone}">${icoSvg(icon, { size: "sm" })}</span><span>${textHTML}</span>${clockHTML(at)}${link}</div>`;
}

// The verdict a run wrote: the visit's verdict by the same agent between its start and a few
// minutes after it finished.
const VERDICT_SLACK_MS = 5 * 60 * 1000;
export function runVerdict(run: RunInfo, visit: Visit): VerdictMark | undefined {
  const end = run.finishedAt ? Date.parse(run.finishedAt) + VERDICT_SLACK_MS : Infinity;
  return [...visit.verdicts]
    .reverse()
    .find(
      (x) =>
        (!x.agent || !run.agent || x.agent === run.agent) &&
        x.at >= run.startedAt &&
        Date.parse(x.at) <= end,
    );
}

// How a run ended as the Task View Lab says it: stopped for a person, sent back, else its state.
function runOutcomeHTML(
  run: RunInfo,
  v: VisitCard,
  verdict: VerdictMark | undefined,
  view: RoundsView,
) {
  const end = run.finishedAt ? Date.parse(run.finishedAt) + NEAR_MS : Infinity;
  const stopped = v.items.some(
    (x) => x.kind === "stop" && x.at >= run.startedAt && Date.parse(x.at) <= end,
  );
  if (stopped)
    return badgeHTML(t("lib.activity_rounds.run.stopped"), "warn", {
      shape: "pill",
      dot: "warn",
      attrs: { "data-run-state": run.state },
    });
  if (verdict?.kickBackTo)
    return badgeHTML(
      t("lib.activity_rounds.run.sent_back", { stage: view.stageLabel(verdict.kickBackTo) }),
      "warn",
      { shape: "pill", dot: "warn", attrs: { "data-run-state": run.state } },
    );
  return runPillHTML(run);
}

function runItemHTML(
  run: RunInfo,
  note: KindedComment | undefined,
  v: VisitCard,
  view: RoundsView,
): string {
  const visit = v.visit;
  const agent = agentName(run.agent);
  const start = run.startedAt;
  const end = run.finishedAt;
  const when = start
    ? end
      ? `${fmtDayClock(start)} → ${fmtClock(end)} · ${spanText(Date.parse(end) - Date.parse(start))}`
      : fmtDayClock(start)
    : "";
  const verdict = runVerdict(run, visit);
  const said = verdictText(verdict);
  const noteHTML = note?.body
    ? (view.noteHTML?.(note.body) ?? esc(note.body))
    : said
      ? esc(said)
      : "";
  const pending = run.state === "queued" || run.state === "not_started";
  const color = view.agentColor?.(run.agent, run.stage);
  const links = [
    verdict?.reportId
      ? linkHTML(t("lib.activity_rounds.run.verdict"), { "data-ar-report": verdict.reportId })
      : "",
    pending ? "" : linkHTML(t("lib.activity.run.view_log"), { "data-run-log": run.id }),
    pending ? "" : linkHTML(t("lib.activity.run.open_chat"), { "data-run-chat": run.id }),
    run.state === "queued"
      ? linkHTML(t("lib.activity.run.cancel_wait"), {
          "data-run-cancel": run.id,
          "data-run-agent": run.agent || "",
          "data-run-stage": run.stage ? view.ctx.stageLabel(run.stage) : "",
        })
      : "",
    run.state === "running"
      ? linkHTML(t("lib.activity.run.stop"), { "data-run-stop": run.id, "data-run-n": run.number })
      : "",
  ].join("");
  return `<div class="ar-vr" data-run="${esc(run.id)}">
      <div class="ar-cl-meta">${cmAvatarHTML("agent", agent, color)}<b>${esc(agent)}</b>${runOutcomeHTML(run, v, verdict, view)}${when ? `<span class="ar-faint">${esc(when)}</span>` : ""}</div>
      ${runNoticesHTML(run, view.ctx).join("")}${noteHTML ? `<div class="ar-vr-note">${noteHTML}</div>` : ""}
      ${links ? `<div class="ar-foot">${links}</div>` : ""}
    </div>`;
}

function itemHTML(x: ActivityItem, v: VisitCard, view: RoundsView): string {
  const c = x.comment;
  const thread = (id: string) => reasonLinkHTML(id, t("lib.activity_rounds.open_thread"));
  switch (x.kind) {
    case "run":
      return x.run ? runItemHTML(x.run, x.note, v, view) : "";
    case "question": {
      if (!c) return "";
      const agent = agentName(c.author ?? "");
      const text = x.reply
        ? boldHTML(
            tParts("lib.activity_rounds.item.question_answered", {
              agent: SLOT,
              who: personName(x.reply.author ?? "", x.reply.source ?? "user", view.viewer),
            }),
            agent,
          )
        : boldHTML(tParts("lib.activity_rounds.item.question", { agent: SLOT }), agent);
      return evHTML("q", "help", text, x.at, thread(c.id));
    }
    case "stop": {
      if (!c) {
        const p = x.event?.payload ?? {};
        const agent = agentName(str(p.agent));
        return evHTML(
          "blk",
          "stop",
          boldHTML(tParts("lib.activity_rounds.item.stop", { agent: SLOT }), agent),
          x.at,
        );
      }
      const agent = agentName(c.author ?? "");
      const reply = x.reply;
      const who = reply ? personName(reply.author ?? "", reply.source ?? "user", view.viewer) : "";
      const decided =
        reply && commentKind(reply) === "decision"
          ? decisionLabel(reply.meta, view.stageLabel).text
          : "";
      const text = reply
        ? decided
          ? boldHTML(
              tParts("lib.activity_rounds.item.stop_decided", {
                agent: SLOT,
                who,
                decision: decided,
              }),
              agent,
            )
          : boldHTML(tParts("lib.activity_rounds.item.stop_answered", { agent: SLOT, who }), agent)
        : boldHTML(tParts("lib.activity_rounds.item.stop", { agent: SLOT }), agent);
      return evHTML("blk", "stop", text, x.at, thread(c.id));
    }
    case "decision": {
      if (!c) return "";
      const d = decisionLabel(c.meta, view.stageLabel);
      const who = personName(c.author ?? "", c.source ?? "user", view.viewer);
      return evHTML(
        "vio",
        d.icon.replace(/^i-/, ""),
        boldHTML(tParts("lib.activity_rounds.item.decision", { who: SLOT, decision: d.text }), who),
        x.at,
        reasonLinkHTML(c.id, t("lib.activity_rounds.reason_short")),
      );
    }
    case "comment": {
      if (!c) return "";
      const who = personName(c.author ?? "", c.source ?? "user", view.viewer);
      return evHTML(
        "vio",
        "user",
        boldHTML(
          tParts("lib.activity_rounds.item.comment", {
            who: SLOT,
            text: excerpt(c.body ?? "", 120),
          }),
          who,
        ),
        x.at,
        thread(c.id),
      );
    }
    case "relaunch": {
      const e = x.event;
      if (!e) return "";
      const p = e.payload ?? {};
      const stage = str(p.stage) ? view.stageLabel(str(p.stage)) : "";
      const turns = typeof p.turns === "number" ? p.turns : Number(p.turns) || 0;
      const who = personName(e.actor ?? "", "user", view.viewer);
      const parts = turns
        ? tParts("lib.activity_rounds.item.relaunch_turns", { who: SLOT, stage, turns })
        : tParts("lib.activity_rounds.item.relaunch", { who: SLOT, stage });
      return evHTML(
        "vio",
        "play",
        boldHTML(parts, who),
        x.at,
        reasonLinkHTML(str(p.comment_id), t("lib.activity_rounds.reason_short")),
      );
    }
    case "access": {
      const e = x.event;
      if (!e) return "";
      const p = e.payload ?? {};
      const cases = Array.isArray(p.cases)
        ? p.cases.filter((k): k is string => typeof k === "string")
        : [];
      const who = personName(e.actor ?? "", "user", view.viewer);
      return evHTML(
        "vio",
        "lock",
        boldHTML(
          tParts("lib.activity_rounds.item.access", { who: SLOT, cases: refsText(cases) || "—" }),
          who,
        ),
        x.at,
        reasonLinkHTML(str(p.comment_id), t("lib.activity_rounds.reason_short")),
      );
    }
    case "system": {
      let entry: SystemEntry | null = null;
      if (x.event)
        entry = {
          id: x.event.id,
          at: x.at,
          kind: classifyEvent(x.event.kind) ?? "other",
          event: x.event,
        };
      else if (c) entry = { id: c.id, at: x.at, kind: "note", note: c };
      if (!entry) return "";
      return `<div class="ar-ev sys">${clockHTML(x.at)}<span>${systemTextHTML(entry, view.ctx)}</span></div>`;
    }
  }
}

// A badge read by its label: role="img" with the same text as the tooltip, so a screen reader
// announces the questions, the stop or the failed runs the icon shows.
function chipHTML(tone: string, icon: string, label: string): string {
  return `<span class="ar-bdg-i ${tone}" role="img" aria-label="${esc(label)}" title="${esc(label)}">${icoSvg(icon, { size: "sm" })}</span>`;
}

function visitRowHTML(v: VisitCard, view: RoundsView): string {
  const look = END_LOOK[v.end.kind];
  const badges = [
    v.questions
      ? chipHTML("q", "help", t("lib.activity_rounds.badge.questions", { count: v.questions }))
      : "",
    v.stops ? chipHTML("blk", "stop", t("lib.activity_rounds.badge.stopped")) : "",
    v.failed
      ? chipHTML("fail", "x", t("lib.activity_rounds.badge.failed", { count: v.failed }))
      : "",
    v.runs.length > 1
      ? `<span class="ar-faint">${esc(t("lib.activity_rounds.badge.runs", { count: v.runs.length }))}</span>`
      : "",
  ].join("");
  const until = v.visit.until ? Date.parse(v.visit.until) : view.ctx.now;
  const when = `${fmtClock(v.visit.at)} · ${spanText(until - Date.parse(v.visit.at))}`;
  const entered =
    v.visit.byPerson && v.visit.from
      ? evHTML(
          "vio",
          "user",
          esc(t("lib.activity_rounds.item.moved_here")),
          "",
          reasonLinkHTML(v.visit.commentID ?? "", t("lib.activity_rounds.reason_short")),
          "entered",
        )
      : "";
  const items = v.items.map((x) => itemHTML(x, v, view)).join("");
  const body =
    entered + items || `<p class="ar-muted">${esc(t("lib.activity_rounds.no_items"))}</p>`;
  const open = view.open.has(v.key);
  return `<details${attrsHTML({ class: "ar-cl", open, "data-ar-visit": v.key })}><summary class="ar-cl-row"><span class="ar-st ${look.st}">${icoSvg(look.icon, { size: "sm" })}</span><span class="ar-cl-stage">${esc(view.stageLabel(v.visit.stage))}</span><span class="ar-cl-title">${esc(visitEndText(v.end, view))}</span><span class="ar-cl-when">${badges}<span title="${esc(fmtDate(v.visit.at))}">${esc(when)}</span></span><span class="ar-chev">${icoSvg("chevron-right", { size: "sm" })}</span></summary><div class="ar-cl-body">${body}</div></details>`;
}

function roundCardHTML(r: RoundCard, view: RoundsView): string {
  const title = t("lib.activity_rounds.round", { n: r.n });
  const link = r.opener.byPerson ? reasonLinkHTML(r.opener.commentID ?? "") : "";
  return `<section class="ar-card ar-round-card" id="ar-round-${r.n}" aria-label="${esc(title)}"><div class="ar-round-hd"><span class="n">${esc(title)}</span><span class="ar-faint" title="${esc(fmtDate(r.at))}">${esc(fmtDayClock(r.at))}</span><span class="why">${roundWhy(r, view, true)}</span>${link}</div>${r.visits.map((v) => visitRowHTML(v, view)).join("")}</section>`;
}

export interface RoundsControls {
  filter: RoundFilter;
  newestFirst: boolean;
  system: boolean;
  shown: number;
  stages: string[];
  agents: string[];
}

export function roundsToolbarHTML(c: RoundsControls, stageLabel: (id: string) => string): string {
  const opt = (value: string, label: string, on: boolean) =>
    `<option${attrsHTML({ value, selected: on })}>${esc(label)}</option>`;
  const select = (name: string, label: string, options: string) =>
    `<select${attrsHTML({ class: "select-sm ar-sel", "data-ar-filter": name, "aria-label": label })}>${options}</select>`;
  const stage = select(
    "stage",
    t("lib.activity_rounds.filter.stage"),
    opt("", t("lib.activity_rounds.filter.all_stages"), !c.filter.stage) +
      c.stages.map((s) => opt(s, stageLabel(s), c.filter.stage === s)).join(""),
  );
  const agent = c.agents.length
    ? select(
        "agent",
        t("lib.activity_rounds.filter.agent"),
        opt("", t("lib.activity_rounds.filter.all_agents"), !c.filter.agent) +
          c.agents.map((a) => opt(a, a, c.filter.agent === a)).join(""),
      )
    : "";
  const order = select(
    "order",
    t("lib.activity_rounds.filter.order"),
    opt("new", t("lib.activity_rounds.filter.newest"), c.newestFirst) +
      opt("old", t("lib.activity_rounds.filter.oldest"), !c.newestFirst),
  );
  const sys = checkHTML({
    label: t("lib.activity_rounds.filter.system"),
    checked: c.system,
    cls: "ar-chk",
    inputAttrs: { "data-ar-filter": "system" },
  });
  const clear =
    c.filter.stage || c.filter.agent
      ? buttonHTML({
          label: t("lib.activity_rounds.filter.clear"),
          variant: "link",
          attrs: { "data-ar-clear": true },
        })
      : "";
  return `<div class="ar-tools">${stage}${agent}${order}${sys}${clear}</div>`;
}

export function roundsHTML(
  rounds: readonly RoundCard[],
  c: RoundsControls,
  view: RoundsView,
): string {
  const filtered = filterRounds(rounds, c.filter);
  const { cards, hidden } = pageRounds(filtered, c.shown, c.newestFirst);
  const more =
    hidden || c.shown > ROUND_STEP
      ? `<div class="ar-more">${
          hidden
            ? buttonHTML({
                label: t("lib.activity_rounds.more", { count: hidden }),
                attrs: { "data-ar-more": true },
              })
            : `<span class="ar-faint">${esc(t("lib.activity_rounds.every_round"))}</span>`
        }${c.shown > ROUND_STEP ? buttonHTML({ label: t("lib.activity_rounds.less"), variant: "link", attrs: { "data-ar-less": true } }) : ""}</div>`
      : "";
  const list = cards.length
    ? cards.map((r) => roundCardHTML(r, view)).join("")
    : emptyStateHTML({
        size: "inline",
        body: rounds.length ? t("lib.activity_rounds.no_match") : t("lib.activity_rounds.empty"),
      });
  return `<div>${sectionHeadHTML({ title: t("lib.activity_rounds.title"), level: 4, asideHTML: `<span class="tv-sec-r">${esc(t("lib.activity_rounds.hint"))}</span>` })}
    ${roundsToolbarHTML(c, view.stageLabel)}
    <div class="ar-feed">${c.newestFirst ? "" : more}${list}${c.newestFirst ? more : ""}</div></div>`;
}

// What the outcome icons on the visits mean.
export function roundsLegendHTML(): string {
  const st = (cls: string, icon: string) =>
    `<span class="ar-st ${cls}">${icoSvg(icon, { size: "sm" })}</span>`;
  return `<div class="ar-legend"><span>${st("pass", "check")} ${esc(t("lib.activity_rounds.legend.passed"))}</span><span>${st("blocked", "back")} ${esc(t("lib.activity_rounds.legend.agent_back"))}</span><span>${st("violet", "user")} ${esc(t("lib.activity_rounds.legend.person"))}</span></div>`;
}

export interface CostFacts {
  cost: string;
  runs: number;
  tokens: string;
  span: string;
}

// Cost and size: what the agents spent, how many runs, the tokens and how long the task has been
// going.
export function costCardsHTML(c: CostFacts): string {
  const card = (v: string, l: string) =>
    `<div class="ar-card ar-cost-c"><div class="v">${esc(v)}</div><div class="l">${esc(l)}</div></div>`;
  return `${sectionHeadHTML({ title: t("lib.activity_rounds.cost.title"), level: 4 })}<div class="ar-cost">${card(c.cost, t("lib.activity_rounds.cost.spent"))}${card(String(c.runs), t("lib.activity_rounds.cost.runs"))}${card(c.tokens, t("lib.activity_rounds.cost.tokens"))}${card(c.span, t("lib.activity_rounds.cost.span"))}</div>`;
}
