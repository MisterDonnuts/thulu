// Rounds of a task: the loop a task makes through its stages. Round 1 starts when the task is
// created; round N+1 starts when the task enters a stage earlier in board order than the one it
// left (a kick-back or a person moving it back). The daemon stamps `round` on stage_entered; older
// events carry none and get it from the same rule here.
//
// The Activity tab draws a card per round (lib/activity-rounds), and Tests puts each case run in
// the round it ran in.

import { splitFrontmatter } from "./reports";

export interface FlowEvent {
  id?: string;
  kind: string;
  created_at: string;
  payload?: Record<string, unknown> | null;
}

export interface VerdictMeta {
  verdict?: string;
  reason?: string;
  kick_back_to?: string;
  headline?: string;
  needs_person?: Array<{ ref?: string; ask?: string; why?: string }> | null;
  delta?: unknown;
  next?: string;
}

export interface FlowReport {
  id: string;
  kind: string;
  stage?: string;
  body?: string;
  created_at: string;
  verdict_meta?: VerdictMeta | null;
}

export interface VerdictMark {
  verdict: string;
  reason: string;
  kickBackTo: string;
  agent: string;
  at: string;
  reportId: string;
  headline: string;
  needsPerson: number;
}

export interface Visit {
  stage: string;
  round: number;
  at: string;
  // When the task left this stage; empty while it is still there.
  until: string;
  from: string;
  via: string;
  byPerson: boolean;
  verdicts: VerdictMark[];
  // The comment that explains the move into this stage (a person's reason); empty when none.
  commentID?: string;
  // How the task left: to which stage, whether a person moved it, and the comment with the reason.
  next?: { stage: string; byPerson: boolean; back: boolean; via: string; commentID?: string };
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const truthy = (v: unknown): boolean => v === true || v === "true";

function byTime<T extends { created_at: string }>(list: readonly T[]): T[] {
  return [...list].sort((a, b) =>
    a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0,
  );
}

function roundOf(p: Record<string, unknown>): number | null {
  const r = p.round;
  const n = typeof r === "number" ? r : typeof r === "string" && r !== "" ? Number(r) : NaN;
  return Number.isInteger(n) && n > 0 ? n : null;
}

// The stage visits of a task, oldest first, each with its round. `order` is the board's stage order;
// a stage missing from it never counts as "earlier".
export function stageVisits(events: readonly FlowEvent[], order: readonly string[]): Visit[] {
  const pos = (id: string) => order.indexOf(id);
  const visits: Visit[] = [];
  let round = 1;
  for (const e of byTime(events)) {
    if (e.kind !== "stage_entered") continue;
    const p = e.payload ?? {};
    const to = str(p.to);
    const from = str(p.from);
    if (!to) continue;
    const stamped = roundOf(p);
    if (stamped !== null) round = stamped;
    else if (pos(from) >= 0 && pos(to) >= 0 && pos(to) < pos(from)) round++;
    const prev = visits.at(-1);
    if (prev) {
      prev.until = e.created_at;
      prev.next = {
        stage: to,
        byPerson: truthy(p.by_person),
        back: pos(prev.stage) >= 0 && pos(to) >= 0 && pos(to) < pos(prev.stage),
        via: str(p.via),
        commentID: str(p.comment_id),
      };
    }
    visits.push({
      stage: to,
      round,
      at: e.created_at,
      until: "",
      from,
      via: str(p.via),
      byPerson: truthy(p.by_person),
      verdicts: [],
      commentID: str(p.comment_id),
    });
  }
  return visits;
}

// The headline a stage verdict report carries: verdict_meta from a daemon that parses it, else the
// verdict.md front matter, else the first line of its body.
export function reportHeadline(r: FlowReport | undefined): string {
  if (!r) return "";
  const h = (r.verdict_meta?.headline ?? "").trim();
  if (h) return h;
  const { meta, body } = splitFrontmatter(r.body ?? "");
  if (meta.headline) return meta.headline;
  const line =
    body
      .split("\n")
      .map((l) =>
        l
          .replace(/^\s*(#{1,6}\s+|[-*>]\s+)/, "")
          .replace(/[*_`]/g, "")
          .trim(),
      )
      .find(Boolean) ?? "";
  return line.length > 90 ? line.slice(0, 89).trimEnd() + "…" : line;
}

const VERDICT_REPORT_SLACK_MS = 5 * 60 * 1000;

function closestReport(
  reports: readonly FlowReport[],
  stage: string,
  at: string,
  taken: Set<string>,
): FlowReport | undefined {
  const when = Date.parse(at);
  let best: FlowReport | undefined;
  let gap = Infinity;
  for (const r of reports) {
    if (taken.has(r.id) || r.stage !== stage) continue;
    if (r.kind !== "stage_verdict" && !r.verdict_meta) continue;
    const d = Math.abs(Date.parse(r.created_at) - when);
    if (d <= VERDICT_REPORT_SLACK_MS && d < gap) {
      best = r;
      gap = d;
    }
  }
  return best;
}

// Visits with their stage verdicts attached, each linked to the report that run wrote.
export function visitsWithVerdicts(
  events: readonly FlowEvent[],
  order: readonly string[],
  reports: readonly FlowReport[] = [],
): Visit[] {
  const visits = stageVisits(events, order);
  const taken = new Set<string>();
  for (const e of byTime(events)) {
    if (e.kind !== "stage_verdict") continue;
    const p = e.payload ?? {};
    const stage = str(p.stage);
    let v: Visit | undefined;
    for (let i = visits.length - 1; i >= 0; i--) {
      if (visits[i].at <= e.created_at && (!stage || visits[i].stage === stage)) {
        v = visits[i];
        break;
      }
    }
    if (!v) continue;
    const rep = closestReport(reports, v.stage, e.created_at, taken);
    if (rep) taken.add(rep.id);
    const meta = rep?.verdict_meta ?? null;
    v.verdicts.push({
      verdict: str(p.verdict) || str(meta?.verdict),
      reason: str(p.reason) || str(meta?.reason),
      kickBackTo: str(p.kick_back_to) || str(meta?.kick_back_to),
      agent: str(p.agent),
      at: e.created_at,
      reportId: rep?.id ?? "",
      headline: reportHeadline(rep),
      needsPerson: (meta?.needs_person ?? []).length,
    });
  }
  return visits;
}

// The round an instant belongs to: the round of the visit open at that time, 1 before any visit.
export function roundAt(visits: readonly Visit[], at: string): number {
  let r = 1;
  for (const v of visits) {
    if (v.at <= at) r = v.round;
    else break;
  }
  return r;
}

export function currentRound(visits: readonly Visit[]): number {
  return visits.at(-1)?.round ?? 1;
}
