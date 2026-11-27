import { newProblems, patchLock } from "./board-rules";
import { t } from "../i18n";
import { api } from "./api";
import { roleWithin } from "./api-keys";
import { sessionInfo } from "./auth";
import { skippableReason, skipWhyText } from "./stage-skips";
import type { Stage } from "./stages";

// A session with no role (no-login install) reads as allowed; the daemon still authorizes every
// write.
export function mayEditBoards(role: string | undefined): boolean {
  return roleWithin("admin", role);
}

export async function sessionMayEditBoards(): Promise<boolean> {
  const me = await sessionInfo();
  return mayEditBoards(me?.role);
}

export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  const out = [...list];
  if (from < 0 || from >= out.length) return out;
  const clamped = Math.max(0, Math.min(out.length - 1, to));
  const [item] = out.splice(from, 1);
  out.splice(clamped, 0, item);
  return out;
}

export function finalIndex(from: number, before: number | null, length: number): number {
  const target = before ?? length;
  return target > from ? target - 1 : target;
}

export function stepStage(stages: readonly Stage[], id: string, delta: -1 | 1): Stage[] | null {
  const from = stages.findIndex((s) => s.id === id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= stages.length) return null;
  return moveItem(stages, from, to);
}

export function placeStage(stages: readonly Stage[], id: string, to: number): Stage[] {
  return moveItem(
    stages,
    stages.findIndex((s) => s.id === id),
    to,
  );
}

export function sameOrder(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}

export interface FlowWarning {
  stage: string;
  kind: "initial_not_first" | "after_terminal";
}

export function flowWarnings(stages: readonly Stage[]): FlowWarning[] {
  const out: FlowWarning[] = [];
  const initial = stages.findIndex((s) => s.initial);
  if (initial > 0) out.push({ stage: stages[initial].id, kind: "initial_not_first" });
  const firstTerminal = stages.findIndex((s) => s.terminal);
  if (firstTerminal >= 0) {
    for (const s of stages.slice(firstTerminal + 1)) {
      if (!s.terminal) out.push({ stage: s.id, kind: "after_terminal" });
    }
  }
  return out;
}

export function flowWarningText(w: FlowWarning, stages: readonly Stage[]): string {
  const label = (id: string) => stages.find((s) => s.id === id)?.label ?? id;
  if (w.kind === "initial_not_first") {
    return t("lib.board_editor.warnings.initial_not_first", { stage: label(w.stage) });
  }
  const terminal = stages.find((s) => s.terminal);
  return t("lib.board_editor.warnings.after_terminal", {
    stage: label(w.stage),
    terminal: terminal?.label ?? "",
  });
}

export const FLAG_KEYS = [
  "initial",
  "terminal",
  "auto_advance",
  "push_branch",
  "open_pr",
  "agent_verdict",
  "lock_tests",
  "pr_draft",
  "pr_ready",
  "merge_target",
  "release_resources",
  "no_skip",
] as const;
export type FlagKey = (typeof FLAG_KEYS)[number];

// The pull request and release flags, shown in their own section of the column dialog.
export const PR_FLAGS: readonly FlagKey[] = [
  "pr_draft",
  "pr_ready",
  "merge_target",
  "release_resources",
];

// Mirror the daemon's validateEdgeFlags: no agent runs in the start or the finishing column.
const EDGE_FLAGS: readonly FlagKey[] = [
  "agent_verdict",
  "lock_tests",
  "release_resources",
  "pr_draft",
];
// A pull request never lands in the start column.
const START_FLAGS: readonly FlagKey[] = ["merge_target", "pr_ready"];

export const WIP_GROUPS = ["", "work", "review"] as const;
export type WipGroup = (typeof WIP_GROUPS)[number];

export interface FlagState {
  checked: boolean;
  disabled: boolean;
  reason: string;
}

// A flag is locked when flipping it would break one of the daemon's board rules (board-rules.ts
// mirrors them, tested against the daemon on shared boards). The start and finishing flags keep
// their own wording.
export function flagState(stage: Stage, flag: FlagKey, stages: readonly Stage[]): FlagState {
  const checked = Boolean(stage[flag]);
  const reason =
    edgeFlagLock(stage, flag, checked, stages) || flipLock(stage, flag, checked, stages);
  return { checked, disabled: reason !== "", reason };
}

function edgeFlagLock(
  stage: Stage,
  flag: FlagKey,
  checked: boolean,
  stages: readonly Stage[],
): string {
  switch (flag) {
    case "initial":
      if (checked) return t("lib.board_editor.locks.initial_move");
      return stage.terminal ? t("lib.board_editor.locks.initial_terminal") : "";
    case "terminal":
      if (checked && stages.filter((s) => s.terminal).length <= 1) {
        return t("lib.board_editor.locks.last_terminal");
      }
      return !checked && stage.initial ? t("lib.board_editor.locks.terminal_initial") : "";
    case "auto_advance":
      return !checked && stage.terminal ? t("lib.board_editor.locks.terminal_no_advance") : "";
    case "push_branch":
    case "open_pr":
    case "agent_verdict":
    case "lock_tests":
    case "pr_draft":
    case "pr_ready":
    case "merge_target":
    case "release_resources":
      return "";
    case "no_skip":
      return checked ? "" : noSkipLock(stage);
  }
}

// Why "can't be skipped" can't be turned on: the column is never skipped anyway (the same rule
// the drawer and the daemon apply), or skip_unless already lets the board skip it.
function noSkipLock(stage: Stage): string {
  const why = skippableReason(stage);
  if (why !== "") return skipWhyText(why);
  return stage.skip_unless?.length ? t("lib.board_editor.locks.no_skip_skip_unless") : "";
}

function flipLock(stage: Stage, flag: FlagKey, checked: boolean, stages: readonly Stage[]): string {
  const board = stages.some((s) => s.id === stage.id) ? stages : [...stages, stage];
  return patchLock(board, stage.id, applyPatch(board, stage.id, flagPatch(stage, flag, !checked)));
}

// Why patching a column this way would break the board; "" when the daemon would take it.
export function stagePatchLock(
  stages: readonly Stage[],
  id: string,
  patch: Partial<Stage>,
): string {
  return patchLock(stages, id, applyPatch(stages, id, patch));
}

// Why no approval can be added to a column that has none yet (it may be skipped); "" when one can.
// A column that already has approvals keeps its add button: the save runs them through
// stagePatchLock.
export function approvalsAddLock(stages: readonly Stage[], stage: Stage): string {
  if (stage.required_approvals?.length) return "";
  return stagePatchLock(stages, stage.id, { required_approvals: [{ key: "new", label: "new" }] });
}

// The columns before `stage` that may receive its changes sent back: the daemon refuses a target
// that may be skipped. The current target stays listed so the select shows it.
export function kickBackChoices(stages: readonly Stage[], stage: Stage): Stage[] {
  const idx = stages.findIndex((s) => s.id === stage.id);
  return stages
    .slice(0, Math.max(idx, 0))
    .filter(
      (s) =>
        s.id === stage.changes_kick_back_to ||
        !newProblems(stages, applyPatch(stages, stage.id, { changes_kick_back_to: s.id })).some(
          (p) => p.rule === "skip_unless_kick_back_target",
        ),
    );
}

// auto_advance must be cleared in the same write, or the daemon refuses the board.
export function flagPatch(stage: Stage, flag: FlagKey, value: boolean): Partial<Stage> {
  const patch: Partial<Stage> = { [flag]: value };
  if (flag === "terminal" && value) {
    if (stage.auto_advance) patch.auto_advance = false;
  }
  // The daemon refuses a start or finishing column that keeps an agent's flags: clear them in the
  // same write.
  if ((flag === "terminal" || flag === "initial") && value) {
    const cleared = flag === "initial" ? [...EDGE_FLAGS, ...START_FLAGS] : EDGE_FLAGS;
    for (const f of cleared) if (stage[f]) patch[f] = false;
    if (stage.skip_unless?.length) patch.skip_unless = [];
  }
  return patch;
}

// Must match the daemon's maxSkipUnless and stage id rule, which skip_unless values follow.
export const MAX_SKIP_UNLESS = 8;

export type SkipUnlessParse = { values: string[] } | { problem: string };

// "ui, api" → ["ui", "api"]. Empty runs the column for every task.
export function parseSkipUnless(raw: string): SkipUnlessParse {
  const values = [
    ...new Set(
      raw
        .split(/[\s,]+/)
        .map((v) => v.trim().toLowerCase())
        .filter(Boolean),
    ),
  ];
  const bad = values.find((v) => !validId(v));
  if (bad) return { problem: t("lib.board_editor.skip_unless.invalid", { value: bad }) };
  if (values.length > MAX_SKIP_UNLESS) {
    return { problem: t("lib.board_editor.skip_unless.too_many", { max: MAX_SKIP_UNLESS }) };
  }
  return { values };
}

export function skipUnlessText(stage: Pick<Stage, "skip_unless">): string {
  return (stage.skip_unless ?? []).join(", ");
}

// Mirrors the daemon: setting `initial` clears it on every other column in the same write.
export function applyPatch(stages: readonly Stage[], id: string, patch: Partial<Stage>): Stage[] {
  return stages.map((s) => {
    if (s.id === id) return { ...s, ...patch, id: s.id };
    if (patch.initial === true && s.initial) return { ...s, initial: false };
    return s;
  });
}

// Must match the stage ids the daemon reserves.
export const RESERVED_STAGE_IDS = ["mention", "chat", "archived", "queued"];

const ID_MAX = 32;

export function slugify(
  label: string,
  taken: Iterable<string> = [],
  fallback = "column",
  reserved: readonly string[] = [],
): string {
  const base =
    label
      .normalize("NFD")
      .replace(/[\u0300-\u036f]/g, "")
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "_")
      .replace(/^_+|_+$/g, "")
      .slice(0, ID_MAX)
      .replace(/_+$/g, "") || fallback;
  const used = new Set([...taken, ...reserved]);
  if (!used.has(base)) return base;
  for (let n = 2; ; n++) {
    const suffix = `_${n}`;
    const candidate = base.slice(0, ID_MAX - suffix.length).replace(/_+$/g, "") + suffix;
    if (!used.has(candidate)) return candidate;
  }
}

// Must match the daemon's stage id rule.
export function validId(id: string): boolean {
  return /^[a-z0-9_]{1,32}$/.test(id);
}

// Stored per stage by the daemon, not theme colors. The first three match the shipped board's
// columns.
export const STAGE_COLORS: ReadonlyArray<{ name: SwatchName; hex: string }> = [
  { name: "slate", hex: "#5d6477" },
  { name: "orange", hex: "#f2994a" },
  { name: "green", hex: "#3aa667" },
  { name: "blue", hex: "#56a8f2" },
  { name: "cyan", hex: "#56ccf2" },
  { name: "purple", hex: "#bb86fc" },
  { name: "pink", hex: "#e573b5" },
  { name: "red", hex: "#e5484d" },
  { name: "yellow", hex: "#f2c94c" },
  { name: "gray", hex: "#8a92a6" },
];
export type SwatchName =
  "slate" | "orange" | "green" | "blue" | "cyan" | "purple" | "pink" | "red" | "yellow" | "gray";

export function normalizeHex(hex: string): string {
  const h = hex.trim().toLowerCase();
  if (/^#[0-9a-f]{3}$/.test(h)) return `#${h[1]}${h[1]}${h[2]}${h[2]}${h[3]}${h[3]}`;
  return h;
}

export function swatchName(hex: string): SwatchName | "" {
  const h = normalizeHex(hex);
  return STAGE_COLORS.find((c) => c.hex === h)?.name ?? "";
}

export function nextColor(items: ReadonlyArray<{ color?: string }>): string {
  const used = new Set(items.map((s) => normalizeHex(s.color ?? "")));
  return (STAGE_COLORS.find((c) => !used.has(c.hex)) ?? STAGE_COLORS[items.length % 10]).hex;
}

export function defaultInsertIndex(stages: readonly Stage[]): number {
  const i = stages.findIndex((s) => s.terminal);
  return i < 0 ? stages.length : i;
}

export function blankStage(id: string, label: string, color: string): Stage {
  return {
    id,
    label,
    color,
    position: 0,
    auto_advance: false,
    blocked_by_flags: false,
    initial: false,
    terminal: false,
    ingest_flags: false,
    push_branch: false,
    open_pr: false,
    wip_group: "",
    virtual: false,
  };
}

export interface BoardApi {
  reorder(board: string, ids: string[]): Promise<Stage[]>;
  patch(board: string, id: string, patch: Partial<Stage>): Promise<Stage[]>;
  create(board: string, stage: Stage, index: number): Promise<Stage[]>;
  remove(board: string, id: string, moveTo: string): Promise<Stage[]>;
}

const qs = (board: string, extra = "") =>
  `?${extra ? `${extra}&` : ""}board=${encodeURIComponent(board)}`;

export const httpBoardApi: BoardApi = {
  reorder: (board, ids) => api.put<Stage[]>(`/stages/order${qs(board)}`, { ids }),
  patch: (board, id, patch) =>
    api.patch<Stage[]>(`/stages/${encodeURIComponent(id)}${qs(board)}`, patch),
  create: (board, stage, index) =>
    api.post<Stage[]>(`/stages${qs(board)}`, {
      id: stage.id,
      label: stage.label,
      color: stage.color,
      index,
    }),
  remove: async (board, id, moveTo) => {
    const extra = moveTo ? `move_to=${encodeURIComponent(moveTo)}` : "";
    const res = await api.del<{ stages: Stage[] }>(
      `/stages/${encodeURIComponent(id)}${qs(board, extra)}`,
    );
    return res.stages;
  },
};

export type EditKind = "reorder" | "patch" | "create" | "remove";

export interface EditResult {
  ok: boolean;
  kind: EditKind;
  stage: string;
  error?: unknown;
  dropped?: boolean;
}

interface Op {
  kind: EditKind;
  stage: string;
  apply: (stages: Stage[]) => Stage[];
  send: (confirmed: Stage[], board: string) => Promise<Stage[]>;
}

export interface BoardEditorOpts {
  board: string;
  stages: Stage[];
  api?: BoardApi;
  onChange?: (stages: Stage[]) => void;
  onError?: (result: EditResult) => void;
}

// Shown = last confirmed board with pending edits replayed. Edits go one at a time in order;
// a failed one drops out of the replay, so a slow answer never paints over a newer edit.
export class BoardEditor {
  private board: string;
  private confirmed: Stage[];
  private pending: Op[] = [];
  private chain: Promise<void> = Promise.resolve();
  // Bumped on every board switch, so A → B → A still drops edits queued on the first A.
  private epoch = 0;
  private readonly api: BoardApi;
  private readonly onChange: (stages: Stage[]) => void;
  private readonly onError: (result: EditResult) => void;

  constructor(opts: BoardEditorOpts) {
    this.board = opts.board;
    this.confirmed = opts.stages;
    this.api = opts.api ?? httpBoardApi;
    this.onChange = opts.onChange ?? (() => undefined);
    this.onError = opts.onError ?? (() => undefined);
  }

  get stages(): Stage[] {
    return this.pending.reduce((acc, op) => op.apply(acc), this.confirmed);
  }

  get boardID(): string {
    return this.board;
  }

  get busy(): boolean {
    return this.pending.length > 0;
  }

  // Ignored while edits for the same board are in flight: their answers carry a newer board.
  reset(board: string, stages: Stage[]): void {
    if (board === this.board && this.busy) return;
    if (board !== this.board) this.epoch++;
    this.board = board;
    this.confirmed = stages;
    this.pending = [];
  }

  reorder(ids: string[]): Promise<EditResult> {
    const byId = (list: Stage[]) => {
      const map = new Map(list.map((s) => [s.id, s]));
      const ordered = ids.map((id) => map.get(id)).filter((s): s is Stage => Boolean(s));
      return [...ordered, ...list.filter((s) => !ids.includes(s.id))];
    };
    return this.run({
      kind: "reorder",
      stage: "",
      apply: byId,
      send: (confirmed, board) =>
        this.api.reorder(
          board,
          byId(confirmed).map((s) => s.id),
        ),
    });
  }

  step(id: string, delta: -1 | 1): Promise<EditResult> | null {
    const next = stepStage(this.stages, id, delta);
    return next ? this.reorder(next.map((s) => s.id)) : null;
  }

  moveTo(id: string, index: number): Promise<EditResult> | null {
    const before = this.stages.map((s) => s.id);
    const next = placeStage(this.stages, id, index).map((s) => s.id);
    return sameOrder(before, next) ? null : this.reorder(next);
  }

  patch(id: string, patch: Partial<Stage>): Promise<EditResult> {
    return this.run({
      kind: "patch",
      stage: id,
      apply: (list) => applyPatch(list, id, patch),
      send: (_confirmed, board) => this.api.patch(board, id, patch),
    });
  }

  rename(id: string, label: string): Promise<EditResult> | null {
    const next = label.trim();
    const cur = this.stages.find((s) => s.id === id);
    if (!next || !cur || cur.label === next) return null;
    return this.patch(id, { label: next });
  }

  recolor(id: string, color: string): Promise<EditResult> | null {
    const cur = this.stages.find((s) => s.id === id);
    if (!cur || normalizeHex(cur.color || "") === normalizeHex(color)) return null;
    return this.patch(id, { color });
  }

  setFlag(id: string, flag: FlagKey, value: boolean): Promise<EditResult> | null {
    const cur = this.stages.find((s) => s.id === id);
    if (!cur) return null;
    const state = flagState(cur, flag, this.stages);
    if (state.disabled || state.checked === value) return null;
    return this.patch(id, flagPatch(cur, flag, value));
  }

  setWipGroup(id: string, group: WipGroup): Promise<EditResult> | null {
    const cur = this.stages.find((s) => s.id === id);
    if (!cur || cur.wip_group === group) return null;
    return this.patch(id, { wip_group: group });
  }

  create(
    label: string,
    opts: { index?: number; color?: string } = {},
  ): { id: string; done: Promise<EditResult> } | null {
    const name = label.trim();
    if (!name) return null;
    const shown = this.stages;
    const id = slugify(
      name,
      shown.map((s) => s.id),
      "column",
      RESERVED_STAGE_IDS,
    );
    const stage = blankStage(id, name, opts.color ?? nextColor(shown));
    const index = Math.max(0, Math.min(shown.length, opts.index ?? defaultInsertIndex(shown)));
    const insert = (list: Stage[]) => {
      if (list.some((s) => s.id === id)) return list;
      const at = Math.min(index, list.length);
      return [...list.slice(0, at), stage, ...list.slice(at)];
    };
    const done = this.run({
      kind: "create",
      stage: id,
      apply: insert,
      send: (confirmed, board) => this.api.create(board, stage, Math.min(index, confirmed.length)),
    });
    return { id, done };
  }

  remove(id: string, moveTo = ""): Promise<EditResult> {
    return this.run({
      kind: "remove",
      stage: id,
      apply: (list) => list.filter((s) => s.id !== id),
      send: (_confirmed, board) => this.api.remove(board, id, moveTo),
    });
  }

  private run(op: Op): Promise<EditResult> {
    this.pending.push(op);
    this.onChange(this.stages);
    // A board switch while this waits drops it unsent: its ids and order belong to the old board.
    const board = this.board;
    const epoch = this.epoch;
    const result = this.chain.then(async (): Promise<EditResult> => {
      if (epoch !== this.epoch) {
        return { ok: false, kind: op.kind, stage: op.stage, dropped: true };
      }
      try {
        const next = await op.send(this.confirmed, board);
        if (epoch === this.epoch) this.confirmed = next;
        this.pending = this.pending.filter((p) => p !== op);
        this.onChange(this.stages);
        return { ok: true, kind: op.kind, stage: op.stage };
      } catch (error) {
        this.pending = this.pending.filter((p) => p !== op);
        this.onChange(this.stages);
        const res = { ok: false, kind: op.kind, stage: op.stage, error };
        this.onError(res);
        return res;
      }
    });
    this.chain = result.then(() => undefined);
    return result;
  }
}

export function errorText(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return t("lib.board_editor.errors.generic");
}
