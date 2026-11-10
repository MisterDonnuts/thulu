import type { RunWordInput } from "./task-card";

export type SubtaskMode = "nested" | "cards";

export interface BoardView {
  types: string[];
  blocked: boolean;
  needsYou: boolean;
  mode: SubtaskMode;
  expanded: string[];
  collapsed: Record<string, boolean>;
  // The list layout's stage groups a person folded or unfolded, by stage id (see groupFolded).
  listFolded: Record<string, boolean>;
}

export function defaultView(): BoardView {
  return {
    types: [],
    blocked: false,
    needsYou: false,
    mode: "nested",
    expanded: [],
    collapsed: {},
    listFolded: {},
  };
}

export function viewKey(board: string): string {
  return `octopus.board.view.${board}`;
}

const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];

function flags(v: unknown): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  if (!v || typeof v !== "object" || Array.isArray(v)) return out;
  for (const [k, b] of Object.entries(v as Record<string, unknown>)) {
    if (typeof b === "boolean") out[k] = b;
  }
  return out;
}

export function parseView(raw: string | null): BoardView {
  const out = defaultView();
  if (!raw) return out;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return out;
  }
  if (!v || typeof v !== "object") return out;
  const o = v as Record<string, unknown>;
  out.types = strings(o.types);
  out.blocked = o.blocked === true;
  out.needsYou = o.needsYou === true;
  out.mode = o.mode === "cards" ? "cards" : "nested";
  out.expanded = strings(o.expanded);
  out.collapsed = flags(o.collapsed);
  out.listFolded = flags(o.listFolded);
  return out;
}

export function activeFilterCount(v: BoardView): number {
  return (v.types.length > 0 ? 1 : 0) + (v.blocked ? 1 : 0) + (v.needsYou ? 1 : 0);
}

export interface FilterTask extends RunWordInput {
  id: string;
  stage: string;
  is_blocked?: boolean;
  blocked_by_count?: number;
}

// The daemon enforces a hard dependency only on the move out of the initial stage.
export function isBlockedCard(t: FilterTask, initialStage: string): boolean {
  return !!t.is_blocked && (Number(t.blocked_by_count) || 0) > 0 && t.stage === initialStage;
}

// Must match RunStatus.NeedsPerson in internal/task/task.go.
const NEEDS_YOU = new Set([
  "awaiting_input",
  "awaiting_approval",
  "failed",
  "out_of_turns",
  "needs_more_turns",
  "blocked",
  "not_started",
  "credential_blocked",
  "no_agent",
  "resume_failed",
]);

export function needsYou(t: FilterTask): boolean {
  return (
    NEEDS_YOU.has(t.status ?? "") ||
    (Number(t.pending_approvals) || 0) > 0 ||
    (Number(t.open_questions) || 0) > 0
  );
}

export interface Groupable {
  id: string;
  parent_id?: string;
  sort_order?: number;
  created_at?: string;
}

export function orderChildren<T extends Groupable>(kids: T[]): T[] {
  const placed = (x: T) => (Number(x.sort_order) || 0) > 0;
  return kids.slice().sort((a, b) => {
    if (placed(a) !== placed(b)) return placed(a) ? -1 : 1;
    const so = (Number(a.sort_order) || 0) - (Number(b.sort_order) || 0);
    if (so !== 0) return so;
    const ca = a.created_at ?? "";
    const cb = b.created_at ?? "";
    if (ca !== cb) return ca < cb ? -1 : 1;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

export interface Grouped<T> {
  top: T[];
  children: Map<string, T[]>;
}

export function groupChildren<T extends Groupable>(all: T[]): Grouped<T> {
  const ids = new Set(all.map((x) => x.id));
  const top: T[] = [];
  const children = new Map<string, T[]>();
  for (const x of all) {
    if (x.parent_id && ids.has(x.parent_id)) {
      const list = children.get(x.parent_id) ?? [];
      list.push(x);
      children.set(x.parent_id, list);
    } else top.push(x);
  }
  for (const [k, list] of children) children.set(k, orderChildren(list));
  return { top, children };
}

export function boardCards<T extends Groupable>(g: Grouped<T>, all: T[], mode: SubtaskMode): T[] {
  return mode === "cards" ? all : g.top;
}

export interface BoardMatch {
  cards: Set<string>;
  kids: Map<string, Set<string>>;
}

export function boardMatches<T extends { id: string }>(
  cards: T[],
  children: Map<string, T[]>,
  mode: SubtaskMode,
  match: (t: T) => boolean,
): BoardMatch {
  const out: BoardMatch = { cards: new Set(), kids: new Map() };
  for (const c of cards) {
    const hits = mode === "nested" ? (children.get(c.id) ?? []).filter(match).map((k) => k.id) : [];
    if (hits.length) out.kids.set(c.id, new Set(hits));
    if (hits.length || match(c)) out.cards.add(c.id);
  }
  return out;
}

export function narrowing(v: BoardView, query: string): boolean {
  return v.blocked || v.needsYou || query.trim() !== "";
}

export const COL_FIT_MIN = 208;
export const COL_STRIP = 44;

export interface CollapseInput {
  manual?: boolean;
  visible: number;
  terminal: boolean;
  dragging: boolean;
  tight: boolean;
}

export function columnCollapsed(c: CollapseInput): boolean {
  if (c.manual !== undefined) return c.manual;
  if (c.visible === 0) return !c.dragging;
  return c.terminal && c.tight;
}

export function boardIsTight(width: number, count: number, gap: number): boolean {
  if (count <= 0 || width <= 0) return false;
  return count * COL_FIT_MIN + (count - 1) * gap > width;
}

export interface DepRef {
  task_id: string;
  title: string;
  stage: string;
  kind: string;
}

export interface BlockerEntry {
  count: number;
  blockers: DepRef[];
}

export type BlockerCache = Map<string, BlockerEntry>;

export function needsBlockerFetch(
  cache: BlockerCache,
  t: { id: string; blocked_by_count?: number },
): boolean {
  const n = Number(t.blocked_by_count) || 0;
  if (n === 0) return false;
  return cache.get(t.id)?.count !== n;
}

export function blockerEntry(
  count: number,
  blockedBy: DepRef[] | undefined,
  isTerminal: (stage: string) => boolean,
): BlockerEntry {
  return {
    count,
    blockers: (blockedBy ?? []).filter((d) => d.kind !== "soft" && !isTerminal(d.stage)),
  };
}

export function blocksCounts(
  cache: BlockerCache,
  live: Array<{ id: string; blocked_by_count?: number }>,
): Map<string, number> {
  const out = new Map<string, number>();
  for (const t of live) {
    const e = cache.get(t.id);
    if (e?.count !== (Number(t.blocked_by_count) || 0)) continue;
    for (const b of e.blockers) out.set(b.task_id, (out.get(b.task_id) ?? 0) + 1);
  }
  return out;
}

export function pruneBlockers(cache: BlockerCache, keep: Set<string>): void {
  for (const id of Array.from(cache.keys())) if (!keep.has(id)) cache.delete(id);
}

export function neighborCard(
  columns: string[][],
  id: string,
  key: "ArrowUp" | "ArrowDown" | "ArrowLeft" | "ArrowRight",
): string | null {
  const col = columns.findIndex((c) => c.includes(id));
  if (col < 0) return null;
  const row = columns[col].indexOf(id);
  if (key === "ArrowUp") return row > 0 ? columns[col][row - 1] : null;
  if (key === "ArrowDown") return row + 1 < columns[col].length ? columns[col][row + 1] : null;
  const step = key === "ArrowLeft" ? -1 : 1;
  for (let c = col + step; c >= 0 && c < columns.length; c += step) {
    const list = columns[c];
    if (list.length) return list[Math.min(row, list.length - 1)];
  }
  return null;
}

export function liftTarget(flow: string[], current: string, dir: -1 | 1): string | null {
  const i = flow.indexOf(current);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= flow.length) return null;
  return flow[j];
}
