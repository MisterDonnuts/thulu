import { t } from "../i18n";
import { agentChipHTML, labelTagHTML, repoChipHTML } from "./chips";
import { needsYou, type FilterTask, type SubtaskMode } from "./board-view";
import { CARD_TAGS } from "./task-card";
import { taskPriorityTagHTML } from "./task-detail";
import {
  attrsHTML,
  buttonHTML,
  esc,
  icoSvg,
  isHexColor,
  tableHeadAttrs,
  tableHeadLabelHTML,
  tagHTML,
  type TableColumn,
  type TableSort,
} from "./ui";

// ── The person's choice ─────────────────────────────────────────────────────────────────────────
// Kept with the account (PUT /me/preferences/tasks.view) and mirrored in this browser, so the page
// opens in the right layout even when the daemon answers late or not at all.

export type TasksLayout = "board" | "list";
export type ListGroup = "stage" | "none";
export type ListSortKey = "title" | "stage" | "status" | "priority" | "updated";
// "" is the board's own order: stage by stage, each column as the board stacks it.
export type ListSort = "" | ListSortKey;
export type SortDir = "asc" | "desc";

// Must match tasksViewPref in internal/api/handle_preferences.go.
export interface TasksViewPref {
  layout: TasksLayout;
  group: ListGroup;
  sort: ListSort;
  dir: SortDir;
}

export const TASKS_VIEW_PREF = "tasks.view";
export const TASKS_VIEW_CACHE = "octopus.tasks.view";

const SORT_KEYS: readonly ListSortKey[] = ["title", "stage", "status", "priority", "updated"];

export function defaultTasksView(): TasksViewPref {
  // Flat, like an issue list: grouping by stage repeats what the stage column already says.
  return { layout: "board", group: "none", sort: "", dir: "asc" };
}

// Each field falls back on its own: one value from a newer daemon does not reset the others.
export function parseTasksView(v: unknown): TasksViewPref {
  const out = defaultTasksView();
  if (!v || typeof v !== "object" || Array.isArray(v)) return out;
  const o = v as Record<string, unknown>;
  if (o.layout === "list" || o.layout === "board") out.layout = o.layout;
  if (o.group === "none" || o.group === "stage") out.group = o.group;
  if (typeof o.sort === "string" && (SORT_KEYS as readonly string[]).includes(o.sort))
    out.sort = o.sort as ListSortKey;
  if (o.dir === "desc" || o.dir === "asc") out.dir = o.dir;
  return out;
}

export function readCachedTasksView(storage: Pick<Storage, "getItem"> | null): TasksViewPref {
  try {
    const raw = storage?.getItem(TASKS_VIEW_CACHE);
    return parseTasksView(raw ? JSON.parse(raw) : null);
  } catch {
    return defaultTasksView();
  }
}

export function cacheTasksView(storage: Pick<Storage, "setItem"> | null, v: TasksViewPref): void {
  try {
    storage?.setItem(TASKS_VIEW_CACHE, JSON.stringify(v));
  } catch {
    // deliberate: the account still has it; this browser just opens on the daemon's answer
  }
}

export function sameTasksView(a: TasksViewPref, b: TasksViewPref): boolean {
  return a.layout === b.layout && a.group === b.group && a.sort === b.sort && a.dir === b.dir;
}

// ── Sorting ─────────────────────────────────────────────────────────────────────────────────────

// The first click sorts the way a person reads the column: A to Z, the board's flow, what needs
// them first, urgent first, newest first. The second reverses it; the third goes back to the
// board's order.
const FIRST_DIR: Record<ListSortKey, SortDir> = {
  title: "asc",
  stage: "asc",
  status: "asc",
  priority: "desc",
  updated: "desc",
};

export function nextSort(
  cur: Pick<TasksViewPref, "sort" | "dir">,
  key: ListSortKey,
): Pick<TasksViewPref, "sort" | "dir"> {
  const first = FIRST_DIR[key];
  if (cur.sort !== key) return { sort: key, dir: first };
  if (cur.dir === first) return { sort: key, dir: first === "asc" ? "desc" : "asc" };
  return { sort: "", dir: "asc" };
}

export function ariaSort(pref: Pick<TasksViewPref, "sort" | "dir">, key: ListSortKey): TableSort {
  if (pref.sort !== key) return "none";
  return pref.dir === "asc" ? "ascending" : "descending";
}

export interface SortableTask extends FilterTask {
  title: string;
  priority?: string;
  updated_at?: string;
  created_at?: string;
}

export interface SortContext {
  stageIndex: (stage: string) => number;
  isTerminal: (stage: string) => boolean;
}

const PRIORITY_RANK: Record<string, number> = { urgent: 4, high: 3, medium: 2, low: 1 };

export function priorityRank(p: string | undefined): number {
  return PRIORITY_RANK[p ?? ""] ?? 0;
}

// Lower first: waiting on a person, then working, then queued, then idle, then done.
export function statusRank(x: SortableTask, terminal: boolean): number {
  if (terminal) return 4;
  if (needsYou(x)) return 0;
  if (x.status === "running") return 1;
  if (x.status === "queued") return 2;
  return 3;
}

function timeOf(x: SortableTask): number {
  // An empty updated_at falls back to created_at, so this is a truthiness test, not a nullish one.
  const stamp = [x.updated_at, x.created_at].find((v) => !!v) ?? "";
  const ms = Date.parse(stamp);
  return Number.isNaN(ms) ? 0 : ms;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

// Stable: rows that tie keep the board's order, so a sort never shuffles equal rows between polls.
export function sortTasks<T extends SortableTask>(
  rows: readonly T[],
  pref: Pick<TasksViewPref, "sort" | "dir">,
  ctx: SortContext,
): T[] {
  const key = pref.sort;
  if (key === "") return rows.slice();
  const sign = pref.dir === "asc" ? 1 : -1;
  const value = (x: T): number | string => {
    switch (key) {
      case "title":
        return x.title;
      case "stage":
        return ctx.stageIndex(x.stage);
      case "status":
        return statusRank(x, ctx.isTerminal(x.stage));
      case "priority":
        return priorityRank(x.priority);
      case "updated":
        return timeOf(x);
    }
  };
  return rows
    .map((x, i) => ({ x, i, v: value(x) }))
    .sort((a, b) => {
      const c =
        typeof a.v === "string" && typeof b.v === "string"
          ? collator.compare(a.v, b.v)
          : (a.v as number) - (b.v as number);
      return c !== 0 ? sign * c : a.i - b.i;
    })
    .map((e) => e.x);
}

// ── Groups and rows ─────────────────────────────────────────────────────────────────────────────

export interface ListStage {
  id: string;
  label: string;
  color?: string;
  terminal?: boolean;
}

export interface ListGroupModel<T> {
  // The stage id, or ALL_GROUP when the list is not grouped.
  id: string;
  stage: ListStage | null;
  tasks: T[];
}

export const ALL_GROUP = "*";

// cards is what the board shows (parents only when subtasks nest); a task on a stage the board does
// not have is not on the board either, so it is left out here too. Empty groups are left out: the
// list shows what there is, and the board is where the empty columns live.
export function listGroups<T extends SortableTask>(
  cards: readonly T[],
  stages: readonly ListStage[],
  pref: Pick<TasksViewPref, "group" | "sort" | "dir">,
  shown: (x: T) => boolean,
  ctx: SortContext,
): Array<ListGroupModel<T>> {
  const known = new Set(stages.map((s) => s.id));
  const visible = cards.filter((x) => known.has(x.stage) && shown(x));
  const byStage = stages.map((s) => ({ s, tasks: visible.filter((x) => x.stage === s.id) }));
  if (pref.group === "none") {
    // The board's own order, read left to right: each column in turn, as it stacks its cards.
    const tasks = pref.sort ? sortTasks(visible, pref, ctx) : byStage.flatMap((b) => b.tasks);
    return tasks.length ? [{ id: ALL_GROUP, stage: null, tasks }] : [];
  }
  const out: Array<ListGroupModel<T>> = [];
  for (const { s, tasks: inStage } of byStage) {
    const tasks = sortTasks(inStage, pref, ctx);
    if (tasks.length) out.push({ id: s.id, stage: s, tasks });
  }
  return out;
}

// A stage that ends the flow starts folded: it fills up, and what is done needs no attention. A
// person's own choice wins.
export function groupFolded(folded: Record<string, boolean>, g: ListGroupModel<unknown>): boolean {
  return folded[g.id] ?? !!g.stage?.terminal;
}

export interface ListRowModel<T> {
  task: T;
  depth: 0 | 1;
  kids: number;
  open: boolean;
  match: boolean;
}

// With nested subtasks a parent's children follow it, indented, while it is open; a search or a
// filter that hits a child opens its parent, as the board's cards do.
export function listRows<T extends { id: string }>(
  tasks: readonly T[],
  children: ReadonlyMap<string, readonly T[]>,
  mode: SubtaskMode,
  expanded: ReadonlySet<string>,
  hits: ReadonlyMap<string, ReadonlySet<string>> | null,
): Array<ListRowModel<T>> {
  const out: Array<ListRowModel<T>> = [];
  for (const task of tasks) {
    const kids = mode === "nested" ? (children.get(task.id) ?? []) : [];
    const hit = hits?.get(task.id);
    const open = kids.length > 0 && (expanded.has(task.id) || !!hit);
    out.push({ task, depth: 0, kids: kids.length, open, match: false });
    if (open)
      for (const k of kids)
        out.push({ task: k, depth: 1, kids: 0, open: false, match: !!hit?.has(k.id) });
  }
  return out;
}

export function rowKey(r: { task: { id: string }; depth: number }): string {
  return `${r.depth}:${r.task.id}`;
}

// ── Markup ──────────────────────────────────────────────────────────────────────────────────────

export type ListColumnID =
  | "task"
  | "stage"
  | "status"
  | "priority"
  | "agent"
  | "projects"
  | "tags"
  | "subtasks"
  | "updated"
  | "actions";

interface ListColumn {
  id: ListColumnID;
  label: () => string;
  sort?: ListSortKey;
  hideLabel?: boolean;
  width?: TableColumn["width"];
}

// The page sheet hides the later columns as the list narrows (see styles/task-list.css); the order
// here is the order they give way in reverse.
export const LIST_COLUMNS: readonly ListColumn[] = [
  { id: "task", label: () => t("tasks.list.columns.task"), sort: "title", width: "fill" },
  { id: "stage", label: () => t("tasks.list.columns.stage"), sort: "stage" },
  { id: "status", label: () => t("tasks.list.columns.status"), sort: "status" },
  { id: "priority", label: () => t("tasks.list.columns.priority"), sort: "priority" },
  { id: "agent", label: () => t("tasks.list.columns.agent") },
  { id: "projects", label: () => t("tasks.list.columns.projects") },
  { id: "tags", label: () => t("tasks.list.columns.tags") },
  { id: "subtasks", label: () => t("tasks.list.columns.subtasks") },
  { id: "updated", label: () => t("tasks.list.columns.updated"), sort: "updated" },
  { id: "actions", label: () => t("tasks.list.columns.actions"), hideLabel: true },
];

// Grouped by stage, every row of a group shares its stage: sorting by it would change nothing.
function sortable(c: ListColumn, pref: Pick<TasksViewPref, "group">): boolean {
  return !!c.sort && !(c.sort === "stage" && pref.group === "stage");
}

export function listHeadHTML(pref: Pick<TasksViewPref, "group" | "sort" | "dir">): string {
  const ths = LIST_COLUMNS.map((c) => {
    const col: TableColumn = {
      label: c.label(),
      hideLabel: c.hideLabel,
      width: c.width,
      key: c.sort,
      sort: sortable(c, pref) && c.sort ? ariaSort(pref, c.sort) : undefined,
      cls: `tl-c-${c.id}`,
    };
    return `<th${attrsHTML(tableHeadAttrs(col))}>${tableHeadLabelHTML(col)}</th>`;
  }).join("");
  return `<tr>${ths}</tr>`;
}

export function listSpan(): number {
  return LIST_COLUMNS.length;
}

export interface GroupHeadView {
  id: string;
  label: string;
  color?: string;
  count: number;
  folded: boolean;
}

export function listGroupHeadHTML(g: GroupHeadView): string {
  const name = g.folded
    ? t("tasks.list.group.show", { stage: g.label, count: g.count })
    : t("tasks.list.group.hide", { stage: g.label });
  const dot = `<span class="tl-dot" aria-hidden="true"${
    isHexColor(g.color ?? "") ? ` style="--tl-dot:${esc(g.color ?? "")}"` : ""
  }></span>`;
  return (
    `<th colspan="${listSpan()}" scope="colgroup">` +
    `<button type="button" class="tl-fold" data-fold="${esc(g.id)}" aria-expanded="${String(!g.folded)}" aria-label="${esc(name)}" title="${esc(name)}">` +
    `${icoSvg("i-chevron-right", { size: "sm", cls: "tl-fold-ico" })}${dot}` +
    `<span class="tl-group-name">${esc(g.label)}</span>` +
    `<span class="tl-group-count">${esc(String(g.count))}</span>` +
    `</button></th>`
  );
}

export interface ListRowView {
  id: string;
  href: string;
  title: string;
  depth: 0 | 1;
  kids: number;
  open: boolean;
  match: boolean;
  typeHTML: string;
  partOf: string;
  blocked: string;
  stage: ListStage;
  // Whether this person may move the task: the stage cell is then the way to change it.
  canMove: boolean;
  stateHTML: string;
  priority?: string;
  agent: string;
  repos: string[];
  tags: string[];
  progressHTML: string;
  age: string;
  updatedISO: string;
  lifted?: boolean;
}

// Like an issue tracker's status: the stage is a button that opens the stage choices in place.
function stageCellHTML(id: string, s: ListStage, canMove: boolean): string {
  const name = s.label || s.id;
  const tag = tagHTML(name, {
    size: "sm",
    color: s.color,
    dot: true,
    title: canMove ? undefined : name,
  });
  if (!canMove) return tag;
  const label = t("tasks.drawer.props.change_stage_named", { stage: name });
  return (
    `<button type="button" class="tl-stage" data-row-stage="${esc(id)}" aria-haspopup="menu" aria-expanded="false" aria-label="${esc(label)}" title="${esc(label)}">` +
    `${tag}${icoSvg("i-chevron-down", { size: "sm", cls: "tl-stage-ico" })}</button>`
  );
}

function tagsCellHTML(tags: string[]): string {
  const shown = tags.slice(0, CARD_TAGS);
  const rest = tags.slice(CARD_TAGS);
  return (
    shown.map((tag) => labelTagHTML(tag, { size: "sm" })).join("") +
    (rest.length
      ? tagHTML(t("lib.task_card.more_tags", { count: rest.length }), {
          size: "sm",
          title: rest.join(", "),
        })
      : "")
  );
}

const NONE = `<span class="tl-none" aria-hidden="true">—</span>`;

function cell(id: ListColumnID, inner: string): string {
  return `<td class="tl-c-${id}">${inner || NONE}</td>`;
}

// The title is a real link to the task, so it opens in a new tab and reads as one to a screen
// reader; the page opens the drawer on a plain click.
export function listRowCellsHTML(r: ListRowView): string {
  const toggle =
    r.kids > 0
      ? buttonHTML({
          label: r.open
            ? t("lib.task_card.labels.hide_subtasks")
            : t("lib.task_card.labels.show_subtasks", { count: r.kids }),
          icon: "chevron-right",
          iconOnly: true,
          size: "xs",
          variant: "ghost",
          cls: "tl-expand",
          attrs: { "aria-expanded": String(r.open), "data-row-expand": r.id },
        })
      : `<span class="tl-expand-gap" aria-hidden="true"></span>`;
  const sub = [
    r.partOf
      ? `<span class="tl-sub-item">${icoSvg("i-corner-up", { size: "sm" })}${esc(t("lib.task_card.part_of", { title: r.partOf }))}</span>`
      : "",
    r.blocked
      ? `<span class="tl-sub-item tl-sub-warn">${icoSvg("i-lock", { size: "sm" })}${esc(r.blocked)}</span>`
      : "",
  ]
    .filter(Boolean)
    .join("");
  const task =
    `<div class="tl-task">${toggle}<span class="tl-type">${r.typeHTML}</span>` +
    `<div class="tl-task-text"><a class="tl-open" href="${esc(r.href)}" data-open-row="${esc(r.id)}">${esc(r.title)}</a>` +
    (sub ? `<span class="tl-sub">${sub}</span>` : "") +
    `</div></div>`;
  const updated = r.age
    ? `<time class="tl-age" datetime="${esc(r.updatedISO)}" title="${esc(new Date(r.updatedISO).toLocaleString())}">${esc(r.age)}</time>`
    : "";
  const menu = buttonHTML({
    label: t("lib.task_card.labels.actions_for", { title: r.title }),
    icon: "more",
    iconOnly: true,
    size: "sm",
    variant: "ghost",
    attrs: { "data-card-menu": "", "aria-haspopup": "menu", "aria-expanded": "false" },
  });
  return [
    `<td class="tl-c-task">${task}</td>`,
    cell("stage", stageCellHTML(r.id, r.stage, r.canMove)),
    cell("status", r.stateHTML),
    cell("priority", taskPriorityTagHTML(r.priority, { size: "sm" })),
    cell("agent", r.agent ? agentChipHTML(r.agent, { size: "sm", title: r.agent }) : ""),
    cell("projects", r.repos.map((name) => repoChipHTML(name, { size: "sm" })).join("")),
    cell("tags", tagsCellHTML(r.tags)),
    cell("subtasks", r.progressHTML),
    cell("updated", updated),
    `<td class="tl-c-actions">${menu}</td>`,
  ].join("");
}

export function listRowClasses(r: Pick<ListRowView, "depth" | "match" | "lifted">): string {
  return ["tl-row", r.depth === 1 && "tl-row-kid", r.match && "is-match", r.lifted && "is-lifted"]
    .filter(Boolean)
    .join(" ");
}

// ── Keyboard ────────────────────────────────────────────────────────────────────────────────────

export function neighborRow(ids: readonly string[], id: string, key: string): string | null {
  const at = ids.indexOf(id);
  if (at < 0) return null;
  if (key === "ArrowUp") return at > 0 ? ids[at - 1] : null;
  if (key === "ArrowDown") return at + 1 < ids.length ? ids[at + 1] : null;
  if (key === "Home") return ids[0] ?? null;
  if (key === "End") return ids[ids.length - 1] ?? null;
  return null;
}
