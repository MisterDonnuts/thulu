import { t } from "../../i18n";
import { buttonHTML, type ButtonOptions } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { emptyRowHTML } from "./empty";

export type TableColumnWidth = "shrink" | "fill" | "title";

export type TableSort = "none" | "ascending" | "descending";

export interface TableColumn {
  label: string;
  num?: boolean;
  width?: TableColumnWidth;
  mono?: boolean;
  hideLabel?: boolean;
  sort?: TableSort;
  key?: string;
  cls?: string;
}

export interface TableCellOptions {
  text?: string | number | null;
  html?: string;
  sub?: string;
  subHTML?: string;
  actions?: ButtonOptions[];
  attrs?: Attrs;
}

export type TableCell = string | number | null | undefined | TableCellOptions;

export type TableRowTone = "danger" | "warn";

export interface TableRow {
  cells: TableCell[];
  tone?: TableRowTone;
  dim?: boolean;
  current?: boolean;
  expanded?: boolean;
  detailHTML?: string;
  id?: string;
  expandLabel?: string;
  cls?: string;
  attrs?: Attrs;
}

export interface TableLook {
  compact?: boolean;
  top?: boolean;
  stack?: boolean;
  noHead?: boolean;
  expandable?: boolean;
  cls?: string;
}

export function tableClasses(look: TableLook = {}): string {
  return [
    "list-table",
    look.top && "list-table-top",
    look.compact && "list-table-compact",
    look.stack && "list-table-stack",
    look.noHead && "list-table-no-head",
    look.cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function tableColumnClasses(col: TableColumn, cell = false): string | undefined {
  const cls = [
    col.num && "num",
    cell && col.mono && "mono",
    col.width && `cell-${col.width}`,
    col.cls,
  ].filter(Boolean);
  return cls.length ? cls.join(" ") : undefined;
}

const SORTS = new Set<string>(["none", "ascending", "descending"]);

export function tableHeadAttrs(col: TableColumn): Attrs {
  if (col.sort !== undefined && !SORTS.has(col.sort)) {
    throw new Error(`table: no sort "${col.sort}"`); // i18n-ignore a programming error
  }
  return {
    scope: "col",
    class: tableColumnClasses(col),
    "data-sort": col.sort !== undefined ? (col.key ?? "") : undefined,
    "aria-sort": col.sort,
  };
}

export function tableHeadLabelHTML(col: TableColumn): string {
  const label = col.hideLabel
    ? `<span class="visually-hidden">${esc(col.label)}</span>`
    : esc(col.label);
  return col.sort !== undefined ? `<button type="button">${label}</button>` : label;
}

export function tableDetailLabel(): string {
  return t("ui.table.details");
}

export function tableHeadHTML(
  cols: TableColumn[],
  look: Pick<TableLook, "expandable"> = {},
): string {
  const caret = look.expandable
    ? `<th scope="col" class="cell-caret"><span class="visually-hidden">${esc(tableDetailLabel())}</span></th>`
    : "";
  const ths = cols.map((c) => `<th${attrsHTML(tableHeadAttrs(c))}>${tableHeadLabelHTML(c)}</th>`);
  return `<thead><tr>${caret}${ths.join("")}</tr></thead>`;
}

function cellOptions(cell: TableCell): TableCellOptions {
  if (cell === null || cell === undefined) return { text: "" };
  if (typeof cell === "object") return cell;
  return { text: cell };
}

export function tableCellHTML(col: TableColumn, cell: TableCell, look: TableLook = {}): string {
  const o = cellOptions(cell);
  const actions = o.actions?.length
    ? `<span class="cell-actions">${o.actions.map((a) => buttonHTML({ ...a, size: "sm" })).join("")}</span>`
    : "";
  const value = o.html ?? esc(o.text === null || o.text === undefined ? "" : String(o.text));
  const subInner = o.sub ? esc(o.sub) : (o.subHTML ?? "");
  const sub = subInner ? `<span class="cell-sub">${subInner}</span>` : "";
  const own: Attrs = {
    class:
      [tableColumnClasses(col, true), actions && "actions-cell"].filter(Boolean).join(" ") ||
      undefined,
    "data-label": look.stack && !col.hideLabel ? col.label : undefined,
  };
  return `<td${attrsHTML(own)}${attrsHTML(o.attrs)}>${value}${sub}${actions}</td>`;
}

export function tableRowHTML(cols: TableColumn[], row: TableRow, look: TableLook = {}): string {
  if (row.cells.length > cols.length) {
    throw new Error(`table: ${row.cells.length} cells for ${cols.length} columns`); // i18n-ignore a programming error
  }
  if (!look.expandable && (row.expanded !== undefined || row.detailHTML !== undefined)) {
    throw new Error("table: a row opens a detail only in an expandable table"); // i18n-ignore a programming error
  }
  const own: Attrs = {
    class: [row.dim && "list-table-dim", row.cls].filter(Boolean).join(" ") || undefined,
    "data-row": row.id,
    "data-tone": row.tone,
    "aria-current": row.current ? "true" : undefined,
  };
  const open = row.expanded === true;
  const caret = look.expandable
    ? `<td class="cell-caret">${buttonHTML({
        label: row.expandLabel ?? tableDetailLabel(),
        icon: "chevron-right",
        iconOnly: true,
        size: "xs",
        variant: "ghost",
        attrs: { "aria-expanded": String(open), "data-expand": row.id },
      })}</td>`
    : "";
  const tds = cols.map((c, i) => tableCellHTML(c, row.cells[i], look)).join("");
  const detail =
    open && row.detailHTML !== undefined
      ? `<tr class="list-table-detail"${attrsHTML({ "data-detail": row.id })}><td colspan="${cols.length + 1}">${row.detailHTML}</td></tr>`
      : "";
  return `<tr${attrsHTML(own)}${attrsHTML(row.attrs)}>${caret}${tds}</tr>${detail}`;
}

export function tableSpan(cols: TableColumn[], look: Pick<TableLook, "expandable"> = {}): number {
  return cols.length + (look.expandable ? 1 : 0);
}

export interface TableOptions extends TableLook {
  empty?: string;
  loading?: boolean;
  label?: string;
  bodyId?: string;
  bare?: boolean;
  id?: string;
  attrs?: Attrs;
}

export function tableHTML(cols: TableColumn[], rows: TableRow[], o: TableOptions = {}): string {
  const body =
    rows.length || (!o.empty && !o.loading)
      ? rows.map((r) => tableRowHTML(cols, r, o)).join("")
      : emptyRowHTML(o.empty, { cols: tableSpan(cols, o), loading: o.loading });
  const own: Attrs = { id: o.id, class: tableClasses(o), "aria-label": o.label };
  const table =
    `<table${attrsHTML(own)}${attrsHTML(o.attrs)}>${tableHeadHTML(cols, o)}` +
    `<tbody${attrsHTML({ id: o.bodyId })}>${body}</tbody></table>`;
  return o.bare ? table : `<div class="table-wrap">${table}</div>`;
}

export function setTableSort(table: HTMLElement, key: string, order: TableSort): void {
  table.querySelectorAll<HTMLElement>("th[data-sort]").forEach((th) => {
    th.setAttribute("aria-sort", th.dataset.sort === key ? order : "none");
  });
}
