import { t } from "../i18n";
import type { Stage, TaskType } from "./stages";
import { taskTypeOf } from "./stages";
import {
  buttonHTML,
  countHTML,
  dotHTML,
  esc,
  icoSvg,
  menuItemsHTML,
  tabsHTML,
  tagHTML,
  type MenuEntry,
  type TagOptions,
} from "./ui";

export interface BoardTask {
  id: string;
  title: string;
  stage: string;
  type_id?: string;
  project_ids?: string[];
  stage_assignments?: Record<string, string>;
}

export function pickBoardID(
  known: Array<{ id: string }>,
  opts: { fromURL?: string | null; remembered?: string | null; fallback?: string },
): string {
  const known_ = (id?: string | null): string | null =>
    id && known.some((b) => b.id === id) ? id : null;
  const fallback = opts.fallback ?? "";
  const first = known[0]?.id ?? "";
  return (known_(opts.fromURL) ?? known_(opts.remembered) ?? fallback) || first || "default";
}

export function matchesTypeFilter(filter: Set<string>, typeID?: string): boolean {
  return filter.size === 0 || filter.has(typeID ?? "");
}

export const ID_SEARCH_MIN = 4;

export function matchesTaskSearch(
  query: string,
  fields: { title: string; projectNames: string[]; agent?: string; id?: string },
): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  const id = (fields.id ?? "").toLowerCase();
  if (id) {
    const hash = q.startsWith("#");
    const prefix = hash ? q.slice(1) : q;
    if (prefix && (hash || prefix.length >= ID_SEARCH_MIN) && id.startsWith(prefix)) return true;
  }
  if (fields.title.toLowerCase().includes(q)) return true;
  if (fields.projectNames.some((n) => n.toLowerCase().includes(q))) return true;
  return (fields.agent ?? "").toLowerCase().includes(q);
}

export interface WipCounts {
  open: number;
  inProgress: number;
  needsReview: number;
}

export function wipCounts(
  tasks: Array<{ stage: string }>,
  stageOf: (id: string) => Stage | undefined,
): WipCounts {
  const counts: WipCounts = { open: 0, inProgress: 0, needsReview: 0 };
  for (const task of tasks) {
    const info = stageOf(task.stage);
    if (info?.terminal) continue;
    counts.open++;
    if (info?.wip_group === "work") counts.inProgress++;
    if (info?.wip_group === "review") counts.needsReview++;
  }
  return counts;
}

export function columnCount(visible: number, total: number): string {
  return visible === total ? String(visible) : `${visible}/${total}`;
}

export function columnEmptyLabel(total: number, stageLabel: string): string {
  return total === 0
    ? t("lib.board.empty.stage", { stage: stageLabel })
    : t("lib.board.empty.filtered");
}

export function wipStatsHTML(c: WipCounts): string {
  const stat = (n: number, word: string) =>
    `<span class="wip-stat"><span class="wip-num">${n}</span> ${esc(word)}</span>`;
  return (
    stat(c.open, t("lib.board.stats.open")) +
    stat(c.inProgress, t("lib.board.stats.in_progress")) +
    stat(c.needsReview, t("lib.board.stats.to_review"))
  );
}

export const COL_W_DEFAULT = 264;
export const COL_W_MIN = 190;
export const COL_W_MAX = 640;

export function clampColumnWidth(w: number): number {
  return Math.max(COL_W_MIN, Math.min(COL_W_MAX, Math.round(w)));
}

export function parseColumnWidths(raw: string | null): Record<string, number> {
  try {
    const parsed: unknown = JSON.parse(raw ?? "{}");
    return parsed && typeof parsed === "object" ? (parsed as Record<string, number>) : {};
  } catch {
    return {};
  }
}

// null drops the override instead of storing the default, so a later COL_W_DEFAULT change still
// applies.
export function withColumnWidth(
  widths: Record<string, number>,
  stageID: string,
  w: number | null,
): Record<string, number> {
  if (w !== null) return { ...widths, [stageID]: w };
  const { [stageID]: _dropped, ...rest } = widths;
  return rest;
}

export function columnDotHTML(): string {
  return dotHTML("neutral", { attrs: { style: "--dot-color:var(--col-color,var(--fg-4))" } });
}

export function columnHeadHTML(s: Stage, extra = ""): string {
  return `
        <div class="col-head">
          <button type="button" class="col-grip" data-col-grip aria-label="${esc(
            t("lib.board_editor.head.move", { column: s.label }),
          )}"><span class="col-grip-dots" aria-hidden="true"></span></button>
          ${columnDotHTML()}
          <span class="col-title">${esc(s.label)}</span>
          ${extra}
          ${buttonHTML({
            iconOnly: true,
            icon: "more",
            label: t("lib.board_editor.head.options", { column: s.label }),
            title: t("lib.board_editor.head.options_title"),
            variant: "ghost",
            cls: "col-menu-btn",
            attrs: { "data-col-menu": true, "aria-haspopup": "menu", "aria-expanded": "false" },
          })}
        </div>`;
}

export function columnShellHTML(s: Stage): string {
  return `${columnHeadHTML(
    s,
    `${countHTML(0, { cls: "col-count" })}
          ${buttonHTML({
            iconOnly: true,
            icon: "chevron-left",
            label: t("lib.board.columns.fold", { column: s.label }),
            title: t("lib.board.columns.fold_hint"),
            variant: "ghost",
            cls: "col-fold",
            attrs: { "data-col-fold": true },
          })}`,
  )}
        <div class="col-cards">
          <div class="col-list"></div>
          ${buttonHTML({
            label: t("lib.board.buttons.new_task"),
            icon: "plus",
            variant: "ghost",
            size: "sm",
            cls: "col-add",
            attrs: { "data-add-stage": s.id },
          })}
        </div>
        <button type="button" class="col-strip" data-col-unfold hidden>
          ${columnDotHTML()}
          <span class="col-strip-name">${esc(s.label)}</span>
          ${countHTML(0, { cls: "col-strip-count" })}
        </button>`;
}

export function columnStripLabel(label: string, count: number): string {
  return t("lib.board.columns.unfold", { column: label, count });
}

export function stageTabsHTML(
  stages: Array<{ id: string; label: string; color?: string }>,
  counts: Record<string, number>,
  active: string,
): string {
  return tabsHTML({
    variant: "pill",
    label: t("tasks.toolbar.stages"),
    idPrefix: "stage-",
    active,
    items: stages.map((s) => ({
      id: s.id,
      label: s.label,
      dot: true,
      dotColor: s.color,
      count: counts[s.id] ?? 0,
    })),
  });
}

export function typeIconHTML(types: TaskType[], typeID: string | undefined, cls = "ico"): string {
  const tt = taskTypeOf(types, typeID);
  if (!tt) return "";
  const inner = tt.icon ? icoSvg(`tt-${tt.icon}`, cls) : `<span class="tt-dot"></span>`;
  const color = tt.color ?? "";
  const style = color ? ` style="--tt-color:${esc(color)}"` : "";
  return `<span class="task-type" title="${esc(tt.label)}"${style}>${inner}</span>`;
}

export function typeTagHTML(
  types: TaskType[],
  typeID: string | undefined,
  opts: Pick<TagOptions, "size" | "cls" | "attrs"> = {},
): string {
  const tt = taskTypeOf(types, typeID);
  if (!tt) return "";
  return tagHTML(tt.label, {
    ...opts,
    inline: true,
    color: tt.color,
    icon: tt.icon ? `tt-${tt.icon}` : undefined,
    dot: !tt.icon,
  });
}

export function typeFilterLabel(types: TaskType[], active: string[]): string {
  if (active.length === 0) return t("lib.board.filters.type_all");
  if (active.length === 1) {
    const id = active[0];
    const label = id === "" ? t("lib.board.labels.untyped") : taskTypeOf(types, id)?.label;
    return t("lib.board.filters.type_one", { type: label ?? id });
  }
  return t("lib.board.filters.type_many", { count: active.length });
}

export function typeFilterItemsHTML(types: TaskType[], active: string[]): string {
  const item = (id: string, label: string, icon: string, color: string): MenuEntry => ({
    title: label,
    role: "menuitemcheckbox",
    checked: active.includes(id),
    keepOpen: true,
    leadHTML: icon
      ? `<span class="tf-icon"${color ? ` style="--tt-color:${esc(color)}"` : ""}>${icon}</span>`
      : undefined,
    indent: !icon,
    cls: "tf-item",
    attrs: { "data-type": id },
  });
  return menuItemsHTML([
    ...types.map((tt) =>
      item(tt.id, tt.label, tt.icon ? icoSvg(`tt-${tt.icon}`) : "", tt.color ?? ""),
    ),
    item("", t("lib.board.labels.untyped"), "", ""),
  ]);
}
