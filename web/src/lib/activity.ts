import { locale, t } from "../i18n";
import { DEV_AUTHOR } from "./person";
import { formatSeconds } from "./time";
import { groupThreads, type Thread, type ThreadComment } from "./threads";

export type ActivityFilter = "all" | "comments" | "runs" | "system";
export const ACTIVITY_FILTERS: readonly ActivityFilter[] = ["all", "comments", "runs", "system"];
export const FILTER_KEY = "octopus.activity.filter";
export const VISIBLE_ITEMS = 12;

export function parseFilter(v: string | null | undefined): ActivityFilter {
  return (ACTIVITY_FILTERS as readonly string[]).includes(v ?? "") ? (v as ActivityFilter) : "all";
}

export interface StreamComment extends ThreadComment {
  created_at: string;
  body?: string;
  stage?: string;
  resolved_at?: string | null;
  resolved_by?: string;
  key?: string;
}

export interface StreamEvent {
  id: string;
  kind: string;
  actor?: string;
  summary?: string;
  created_at: string;
  ref_id?: string;
  payload?: Record<string, unknown> | null;
}

export interface StreamChild {
  id: string;
  title: string;
  created_at?: string;
}

export type RunState =
  "running" | "queued" | "done" | "failed" | "ended" | "not_started" | "stopped" | "out_of_turns";

export interface RunInfo {
  id: string;
  number: number;
  agent: string;
  stage: string;
  auditor: boolean;
  startedAt: string;
  finishedAt: string;
  state: RunState;
  exit: number | null;
  containerID: string;
  reason: string;
  cause?: string;
  wait?: string;
  turns?: number | null;
  maxTurns?: number | null;
  stoppedBy?: string;
}

export interface LiveState {
  status?: string;
  alive?: boolean;
}

const START = new Set(["agent_run_started", "auditor_run_started"]);
const FINISH = new Set(["agent_run_finished", "auditor_run_finished"]);

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number | null => {
  const n = typeof v === "number" ? v : typeof v === "string" && v !== "" ? Number(v) : NaN;
  return isFinite(n) && n > 0 ? n : null;
};

export function localized(p: Record<string, unknown>, field = "reason"): string {
  const all = p[`${field}_i18n`];
  if (all && typeof all === "object") {
    const mine = (all as Record<string, unknown>)[locale];
    if (typeof mine === "string" && mine) return mine;
  }
  return str(p[field]);
}

function endState(p: Record<string, unknown>, exit: number | null): RunState {
  if ("stopped_by" in p) return "stopped";
  if (p.outcome === "max_turns") return "out_of_turns";
  return exit === null || exit === 0 ? "done" : "failed";
}

export function runsFromEvents(events: StreamEvent[], live: LiveState = {}): RunInfo[] {
  const sorted = [...events].sort(byTime);
  const runs: RunInfo[] = [];
  const at = new Map<string, number>();
  let started = 0;
  for (const e of sorted) {
    const p = e.payload ?? {};
    const inst = str(p.instance_id) || (e.ref_id ?? "");
    if (START.has(e.kind) && inst) {
      at.set(inst, runs.length);
      runs.push({
        id: inst,
        number: ++started,
        agent: str(p.agent) || (e.actor ?? "").replace(/^agent:/, ""),
        stage: str(p.stage),
        auditor: e.kind === "auditor_run_started",
        startedAt: e.created_at,
        finishedAt: "",
        state: "ended",
        exit: null,
        containerID: str(p.container_id),
        reason: "",
        maxTurns: num(p.max_turns),
      });
    } else if (FINISH.has(e.kind) && inst) {
      const i = at.get(inst);
      if (i === undefined) continue;
      const exit = typeof p.exit === "number" ? p.exit : null;
      runs[i] = {
        ...runs[i],
        finishedAt: e.created_at,
        exit,
        state: endState(p, exit),
        containerID: runs[i].containerID || str(p.container_id),
        turns: num(p.turns),
        stoppedBy: str(p.stopped_by),
      };
    } else if (e.kind === "run_not_started") {
      runs.push({
        id: e.id,
        number: 0,
        agent: str(p.agent),
        stage: str(p.stage),
        auditor: false,
        startedAt: e.created_at,
        finishedAt: e.created_at,
        state: "not_started",
        exit: null,
        containerID: "",
        reason: localized(p),
        cause: str(p.cause),
      });
    }
  }
  const alive = !!live.alive || live.status === "running";
  for (let i = runs.length - 1; i >= 0; i--) {
    if (runs[i].finishedAt) continue;
    if (i === runs.length - 1 && alive) runs[i] = { ...runs[i], state: "running" };
  }
  if (live.status === "queued") {
    const lastStart = runs.length ? runs[runs.length - 1].startedAt : "";
    const q = [...sorted].reverse().find((e) => e.kind === "run_queued");
    if (q && (!lastStart || q.created_at >= lastStart)) {
      const p = q.payload ?? {};
      runs.push({
        id: q.id,
        number: 0,
        agent: str(p.agent),
        stage: str(p.stage),
        auditor: false,
        startedAt: q.created_at,
        finishedAt: "",
        state: "queued",
        exit: null,
        containerID: "",
        reason: localized(p),
        wait: str(p.wait),
      });
    }
  }
  return runs;
}

export function turnsText(run: RunInfo): string {
  const used = run.turns ?? null;
  if (used === null) return "";
  return run.maxTurns
    ? t("lib.activity.run.turns_of", { used, max: run.maxTurns })
    : t("lib.activity.run.turns", { count: used });
}

export function stoppedText(run: RunInfo, viewer?: Viewer | null): string {
  const who = (run.stoppedBy ?? "").trim();
  if (!who || who === DEV_AUTHOR) return t("lib.activity.run.stopped_by_someone");
  if (viewer && [viewer.name, viewer.email, viewer.id].some((v) => v && v === who))
    return t("lib.activity.run.stopped_by_you");
  return t("lib.activity.run.stopped_by", { who });
}

export function runSeconds(run: RunInfo, now = Date.now()): number {
  const start = Date.parse(run.startedAt);
  const end = run.finishedAt ? Date.parse(run.finishedAt) : now;
  if (!isFinite(start) || !isFinite(end)) return 0;
  return Math.max(0, Math.round((end - start) / 1000));
}

export function runDurationText(run: RunInfo, now = Date.now()): string {
  return formatSeconds(runSeconds(run, now));
}

interface ToolBlock {
  type?: string;
  name?: string;
  input?: unknown;
}

export function lastToolUse(content: unknown): ToolBlock | null {
  if (!Array.isArray(content)) return null;
  for (let i = content.length - 1; i >= 0; i--) {
    const b = content[i] as ToolBlock | null | undefined;
    if (b?.type === "tool_use" && b.name) return b;
  }
  return null;
}

export type SystemKind = "stage" | "run" | "dep" | "subtask" | "note" | "other";

export function classifyEvent(kind: string): SystemKind | null {
  switch (kind) {
    case "stage_entered":
      return "stage";
    case "dep_added":
    case "dep_removed":
    case "dep_resolved":
      return "dep";
    case "subtasks_reordered":
      return "subtask";
    case "task_created":
    case "section_updated":
    case "checklist_toggled":
    case "branch_created":
    case "pr_opened":
    case "branch_merged":
    case "test_case_added":
    case "test_case_updated":
    case "test_case_deleted":
    case "test_case_run":
    case "approval_requested":
    case "approval_decided":
    case "approval_reset":
    case "approval_waived":
    case "approval_deleted":
    case "thread_resolved":
    case "thread_applied":
    case "flag_raised":
    case "flag_resolved":
    case "auditor_verdict":
    case "credential_blocked":
    case "preview_url":
      return "other";
    default:
      return null;
  }
}

export interface SystemEntry {
  id: string;
  at: string;
  kind: SystemKind;
  event?: StreamEvent;
  run?: RunInfo;
  child?: StreamChild;
  note?: StreamComment;
}

const CARD_NOTES = new Set([
  "task.comments.launched",
  "task.comments.launched_resumed",
  "task.comments.run_finished",
  "task.comments.run_stopped",
  "task.comments.run_queued",
  "task.comments.mention_started",
  "api.chats.comments.mention_started",
  "task.comments.launch_failed",
  "task.comments.launch_blocked",
  "task.comments.run_quota_exceeded",
  "task.comments.agent_type_not_found",
  "task.comments.agent_invalid_for_stage",
  "task.comments.stage_no_agent",
  "task.comments.launch_bad_stage_id",
  "task.comments.agent_opened_pr",
  "task.comments.force_pr_opened",
  "task.comments.fallback_pr_opened",
  "task.comments.pr_linked_existing",
]);

export function prOpenedText(p: Record<string, unknown>): string {
  if (p.source === "agent") return t("lib.activity.sys.pr_by_agent");
  if (p.trigger === "force_pr") return t("lib.activity.sys.pr_by_force");
  if (p.discovered === "true") return t("lib.activity.sys.pr_discovered");
  return t("lib.activity.sys.pr_by_octopus");
}

export function prRef(url: string): string {
  const m = /github\.com\/[^/]+\/([^/]+)\/pull\/(\d+)/.exec(url);
  return m ? `${m[1]}#${m[2]}` : url;
}

export function isNoteLine(c: { source?: string; key?: string }): boolean {
  return c.source === "system" && !!c.key && !CARD_NOTES.has(c.key);
}

export function noteLink(key: string | undefined): { href: string; label: string } | null {
  switch (key ?? "") {
    case "task.comments.mention_launch_no_credential":
    case "task.comments.run_engine_auth_failed":
      return { href: "/settings/#llm", label: t("lib.activity.link.ai_engines") };
    case "task.comments.launch_blocked":
      return { href: "/credentials/", label: t("lib.activity.link.credentials") };
    case "task.comments.mention_not_supported":
    case "task.comments.mention_launch_failed":
    case "api.chats.comments.mention_failed":
    case "task.comments.mention_failed_hint":
    case "task.comments.agent_type_not_found":
    case "task.comments.agent_invalid_for_stage":
    case "task.comments.stage_no_agent":
    case "task.comments.auto_advanced_no_agent":
    case "api.tasks.comments.mention_no_prompt":
    case "api.tasks.comments.agent_turn_limit":
      return { href: "/agents/", label: t("lib.activity.link.agents") };
    default:
      return null;
  }
}

export type StreamItem<C extends StreamComment = StreamComment> =
  | { type: "thread"; id: string; at: string; thread: Thread<C>; open: boolean; resolved: boolean }
  | { type: "run"; id: string; at: string; run: RunInfo }
  | { type: "system"; id: string; at: string; entry: SystemEntry }
  | { type: "group"; id: string; at: string; day: string; entries: SystemEntry[] };

export interface StreamInput<C extends StreamComment> {
  comments: C[];
  events: StreamEvent[];
  runs: RunInfo[];
  children?: StreamChild[];
  notes?: C[];
  filter: ActivityFilter;
}

function byTime(a: { created_at: string }, b: { created_at: string }): number {
  return a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0;
}

export function isOpenThread<C extends StreamComment>(th: Thread<C>): boolean {
  return th.replies.length > 0 && !th.root.resolved_at;
}

export function openThreadCount<C extends StreamComment>(comments: C[]): number {
  return groupThreads(comments).filter(isOpenThread).length;
}

export function buildStream<C extends StreamComment>(input: StreamInput<C>): Array<StreamItem<C>> {
  const { filter } = input;
  const items: Array<StreamItem<C>> = [];
  if (filter === "all" || filter === "comments") {
    for (const th of groupThreads(input.comments)) {
      items.push({
        type: "thread",
        id: th.root.id,
        at: th.root.created_at,
        thread: th,
        open: isOpenThread(th),
        resolved: !!th.root.resolved_at,
      });
    }
  }
  if (filter !== "comments") {
    const newest = input.runs.length ? input.runs[input.runs.length - 1].id : "";
    for (const run of input.runs) {
      const card =
        filter === "runs" || (filter === "all" && (run.state !== "done" || run.id === newest));
      if (card) items.push({ type: "run", id: `run-${run.id}`, at: run.startedAt, run });
      else if (run.state === "done")
        items.push({
          type: "system",
          id: `run-${run.id}`,
          at: run.finishedAt || run.startedAt,
          entry: { id: `run-${run.id}`, at: run.finishedAt || run.startedAt, kind: "run", run },
        });
    }
  }
  if (filter === "all" || filter === "system") {
    for (const e of input.events) {
      const kind = classifyEvent(e.kind);
      if (!kind || saidByNote(e)) continue;
      items.push({
        type: "system",
        id: `ev-${e.id}`,
        at: e.created_at,
        entry: { id: `ev-${e.id}`, at: e.created_at, kind, event: e },
      });
    }
    for (const n of input.notes ?? []) {
      items.push({
        type: "system",
        id: `note-${n.id}`,
        at: n.created_at,
        entry: { id: `note-${n.id}`, at: n.created_at, kind: "note", note: n },
      });
    }
    for (const c of input.children ?? []) {
      if (!c.created_at) continue;
      items.push({
        type: "system",
        id: `sub-${c.id}`,
        at: c.created_at,
        entry: { id: `sub-${c.id}`, at: c.created_at, kind: "subtask", child: c },
      });
    }
  }
  const ordered = items
    .map((it, i) => ({ it, i }))
    .sort((a, b) => (a.it.at < b.it.at ? -1 : a.it.at > b.it.at ? 1 : a.i - b.i))
    .map((x) => x.it);
  return filter === "all" ? groupSystem(ordered) : ordered;
}

function saidByNote(e: StreamEvent): boolean {
  const p = e.payload ?? {};
  if (e.kind === "auditor_verdict")
    return typeof p.verdict === "string" && p.verdict !== "" && p.verdict !== "continue";
  if (e.kind === "stage_entered") return p.via === "kick_back";
  return false;
}

export function dayKey(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function groupSystem<C extends StreamComment>(
  items: Array<StreamItem<C>>,
): Array<StreamItem<C>> {
  const lastRun = items.map((it) => it.type).lastIndexOf("run");
  const out: Array<StreamItem<C>> = [];
  let run: Array<Extract<StreamItem<C>, { type: "system" }>> = [];
  const flush = () => {
    if (run.length === 1) out.push(run[0]);
    else if (run.length > 1) {
      const last = run[run.length - 1];
      out.push({
        type: "group",
        id: `grp-${run[0].id}`,
        at: last.at,
        day: dayKey(run[0].at),
        entries: run.map((r) => r.entry),
      });
    }
    run = [];
  };
  items.forEach((it, i) => {
    if (it.type === "system" && lastRun >= 0 && i > lastRun && it.entry.kind === "note") {
      flush();
      out.push(it);
      return;
    }
    if (it.type === "system" && (run.length === 0 || dayKey(run[0].at) === dayKey(it.at))) {
      run.push(it);
      return;
    }
    flush();
    if (it.type === "system") run.push(it);
    else out.push(it);
  });
  flush();
  return out;
}

export function foldStream<T extends { type: string; open?: boolean }>(
  items: T[],
  expanded: boolean,
  keep = VISIBLE_ITEMS,
): { shown: T[]; hidden: number } {
  if (expanded || items.length <= keep) return { shown: items, hidden: 0 };
  const from = items.length - keep;
  const shown: T[] = [];
  let hidden = 0;
  items.forEach((it, i) => {
    if (i >= from || (it.type === "thread" && it.open)) shown.push(it);
    else hidden++;
  });
  return { shown, hidden };
}

let listFormat: Intl.ListFormat | undefined;
function joinList(parts: string[]): string {
  listFormat ??= new Intl.ListFormat(locale, { style: "long", type: "conjunction" });
  return listFormat.format(parts);
}

export function groupSummary(entries: SystemEntry[]): string {
  const n: Record<SystemKind, number> = {
    stage: 0,
    run: 0,
    dep: 0,
    subtask: 0,
    note: 0,
    other: 0,
  };
  for (const e of entries) n[e.kind]++;
  const parts: string[] = [];
  if (n.stage) parts.push(t("lib.activity.group.stage", { count: n.stage }));
  if (n.run) parts.push(t("lib.activity.group.run", { count: n.run }));
  if (n.dep) parts.push(t("lib.activity.group.dep", { count: n.dep }));
  if (n.subtask) parts.push(t("lib.activity.group.subtask", { count: n.subtask }));
  if (n.note) parts.push(t("lib.activity.group.note", { count: n.note }));
  if (n.other) parts.push(t("lib.activity.group.other", { count: n.other }));
  return joinList(parts);
}

let relFormat: Intl.RelativeTimeFormat | undefined;
let shortDate: Intl.DateTimeFormat | undefined;

export function relTime(iso: string, now = Date.now()): string {
  const then = Date.parse(iso);
  if (!isFinite(then)) return "";
  const secs = Math.round((now - then) / 1000);
  if (secs < 45) return t("lib.activity.just_now");
  relFormat ??= new Intl.RelativeTimeFormat(locale, { numeric: "auto", style: "short" });
  if (secs < 3600) return relFormat.format(-Math.max(1, Math.round(secs / 60)), "minute");
  if (secs < 86400) return relFormat.format(-Math.round(secs / 3600), "hour");
  if (secs < 7 * 86400) return relFormat.format(-Math.round(secs / 86400), "day");
  shortDate ??= new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" });
  return shortDate.format(new Date(then));
}

export function dayLabel(iso: string, now = Date.now()): string {
  const key = dayKey(iso);
  if (key === dayKey(new Date(now).toISOString())) return t("lib.activity.today");
  if (key === dayKey(new Date(now - 86400000).toISOString())) return t("lib.activity.yesterday");
  shortDate ??= new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" });
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "" : shortDate.format(d);
}

export interface Viewer {
  name?: string;
  email?: string;
  id?: string;
}

// Must match the daemon's auth.DevModeAuthorName.
export const DEV_MODE_AUTHOR = "user";

export function viewerFor(user: Viewer | null | undefined, devSession: boolean): Viewer | null {
  return user ?? (devSession ? { name: DEV_MODE_AUTHOR } : null);
}

export function isViewer(signature: string | undefined, viewer?: Viewer | null): boolean {
  const a = (signature ?? "").trim();
  return !!a && !!viewer && [viewer.name, viewer.email, viewer.id].some((v) => v && v === a);
}

export function authorLabel(
  c: { author?: string; source?: string },
  viewer?: Viewer | null,
): string {
  const a = (c.author ?? "").trim();
  if (isViewer(a, viewer)) return t("lib.activity.you");
  if (c.source === "automation" && (!a || a === "automation")) return t("lib.activity.automation");
  if (a && !(a === DEV_MODE_AUTHOR && c.source === "user")) return a;
  if (c.source === "agent") return t("lib.activity.an_agent");
  if (c.source === "system") return t("lib.activity.octopus");
  return t("lib.activity.someone");
}

export function initials(name: string): string {
  const base = name.includes("@") ? name.split("@")[0] : name;
  const words = base.split(/[\s._-]+/).filter(Boolean);
  if (words.length === 0) return "?";
  const letters = words.length === 1 ? words[0].slice(0, 1) : words[0][0] + words[1][0];
  return letters.toUpperCase();
}

export function firstLine(body: string, max = 120): string {
  const line =
    body
      .split("\n")
      .map((l) =>
        l
          .replace(/^\s*(#{1,6}\s+|[-*>]\s+|\d+\.\s+)/, "")
          .replace(/[*_`]/g, "")
          .trim(),
      )
      .find((l) => l.length > 0) ?? "";
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line;
}

export function subtaskTitle(body: string, max = 120): string {
  let line = firstLine(body, 1000);
  if (line.endsWith(":")) {
    line = line.slice(0, -1);
    const lastStop = line.search(/[.!?]\s+[^.!?]*$/);
    if (lastStop > 0) line = line.slice(0, lastStop);
  }
  line = line.replace(/\.$/, "").trim();
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line;
}

export function mentionsIn(text: string, handles: string[]): string[] {
  const known = new Map(handles.map((h) => [h.toLowerCase(), h]));
  const out: string[] = [];
  for (const m of text.matchAll(/(^|\s)@([a-zA-Z0-9_-]+)/g)) {
    const h = known.get(m[2].toLowerCase());
    if (h && !out.includes(h)) out.push(h);
  }
  return out;
}

export interface AudienceInput {
  text: string;
  handles: string[];
  notify?: { agent: string; on: boolean };
  fallback?: string;
}

export function audienceLine(input: AudienceInput): string {
  const base = t("lib.activity.audience.workspace");
  const named = mentionsIn(input.text, input.handles);
  const to = named.length ? named : input.fallback ? [input.fallback] : [];
  if (to.length) {
    const who = joinList(to.map((h) => `@${h}`));
    return `${base} ${
      to.length === 1
        ? t("lib.activity.audience.replies_one", { who })
        : t("lib.activity.audience.replies_many", { who })
    }`;
  }
  if (input.notify?.on)
    return `${base} ${t("lib.activity.audience.notified", { agent: input.notify.agent || t("lib.activity.the_agent") })}`;
  return base;
}

export interface MentionableAgent {
  name?: string;
  type?: string;
  valid_stages?: string[] | null;
}

// Mirrors the daemon's types.AgentType.AnswersMentions.
export function answersMentions(a: MentionableAgent): boolean {
  const stages = a.valid_stages ?? [];
  return stages.length === 0 || stages.includes("mention") || stages.includes("chat");
}

export function mentionableAgents<A extends MentionableAgent>(agents: A[]): A[] {
  return agents.filter(answersMentions);
}

export interface ApplyTask {
  stage: string;
  stage_assignments?: Record<string, string> | null;
  run_alive?: boolean;
  session_agent?: string;
}

export interface ApplyTargetResult {
  agent: string;
  gone: string;
}

// Mirrors the daemon's task.Dispatcher.ConverseTarget. Omit `exists` when the agent list failed to
// load; the daemon still refuses on its own.
export function applyTarget(
  task: ApplyTask,
  exists?: (agent: string) => boolean,
): ApplyTargetResult {
  if (task.run_alive) {
    const agent = task.stage_assignments?.[task.stage] ?? "";
    return { agent: agent !== "" ? agent : t("lib.activity.the_agent"), gone: "" };
  }
  const agent = task.session_agent ?? "";
  if (agent !== "" && exists && !exists(agent)) return { agent: "", gone: agent };
  return { agent, gone: "" };
}

export function threadEventText(ev: StreamEvent, titleOf: (threadID: string) => string): string {
  const p = ev.payload ?? {};
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const title = titleOf(str(p.thread_id));
  const thread = title
    ? t("lib.activity.thread_event.thread", { title })
    : t("lib.activity.thread_event.thread_unnamed");
  const actor = ev.actor ?? "";
  const named = str(p.agent) || (actor.startsWith("agent:") ? actor.slice(6) : "");
  const agent = named || t("lib.activity.the_agent");
  switch (ev.kind) {
    case "thread_resolved":
      return p.resolved === false
        ? t("lib.activity.thread_event.reopened", {
            who: actor || t("lib.activity.someone"),
            thread,
          })
        : t("lib.activity.thread_event.resolved", {
            who: actor || t("lib.activity.someone"),
            thread,
          });
    case "thread_applied": {
      const how: Record<string, string> = {
        steered: "lib.activity.thread_event.applied_steered",
        queued: "lib.activity.thread_event.applied_queued",
        continuation: "lib.activity.thread_event.applied_continuation",
        unreachable: "lib.activity.thread_event.applied_unreachable",
      };
      if (!named) return t("lib.activity.thread_event.applied_nobody", { thread });
      const key = how[str(p.delivery)];
      return key
        ? t(key, { agent, thread })
        : t("lib.activity.thread_event.applied", { agent, thread });
    }
    case "mention_started":
      return t("lib.activity.thread_event.mention_started", { agent, thread });
    case "mention_finished":
      if (p.posted === false)
        return t("lib.activity.thread_event.mention_silent", { agent, thread });
      if (typeof p.exit === "number" && p.exit !== 0)
        return t("lib.activity.thread_event.mention_failed", { agent, thread, code: p.exit });
      return t("lib.activity.thread_event.mention_answered", { agent, thread });
    default:
      return "";
  }
}
