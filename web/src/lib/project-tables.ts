import { t } from "../i18n";
import { costHTML } from "./format";
import { fmtDur, fmtNum, fmtUSD } from "./metrics-format";
import { emptyRowHTML, esc, meterHTML, tableRowHTML, tableSpan, type TableColumn } from "./ui";

export interface DimensionRow {
  key: string;
  sessions: number;
  tasks: number;
  total_tokens: number;
  cost_usd: number;
  estimated_cost_usd?: number;
  unpriced_sessions?: number;
  cost_per_task: number;
}

export interface TaskRow {
  task_id: string;
  title?: string;
  sessions: number;
  turns: number;
  total_tokens: number;
  cost_usd: number;
  estimated_cost_usd?: number;
  unpriced_sessions?: number;
  total_duration_ms: number;
}

export function dimColumns(first: "stage" | "model"): TableColumn[] {
  return [
    {
      label: first === "model" ? t("project.columns.model") : t("project.columns.stage"),
      mono: true,
    },
    { label: t("project.columns.sessions"), num: true },
    { label: t("project.columns.tokens"), num: true },
    { label: t("project.columns.cost_per_task"), num: true },
    { label: t("project.columns.cost"), num: true },
  ];
}

export function topTaskColumns(): TableColumn[] {
  return [
    { label: t("project.columns.task") },
    { label: t("project.columns.runs"), num: true },
    { label: t("project.columns.turns"), num: true },
    { label: t("project.columns.tokens"), num: true },
    { label: t("project.columns.agent_time"), num: true },
    { label: t("project.columns.cost"), num: true },
  ];
}

const LOOK = { stack: true } as const;

function noData(cols: TableColumn[]): string {
  return emptyRowHTML(t("project.tables.no_data"), { cols: tableSpan(cols) });
}

export function dimRowsHTML(cols: TableColumn[], rows: DimensionRow[]): string {
  if (!rows.length) return noData(cols);
  const total = rows.reduce((s, r) => s + r.cost_usd, 0) || 1;
  return rows
    .map((r) => {
      const share = meterHTML((r.cost_usd / total) * 100, {
        size: "xs",
        inline: true,
        label: t("project.tables.cost_share"),
        cls: "dim-share",
      });
      return tableRowHTML(
        cols,
        {
          cells: [
            r.key || "—",
            r.sessions,
            fmtNum(r.total_tokens),
            r.tasks ? fmtUSD(r.cost_per_task) : "—",
            {
              html: costHTML(r.cost_usd, r.estimated_cost_usd, fmtUSD, r.unpriced_sessions) + share,
            },
          ],
        },
        LOOK,
      );
    })
    .join("");
}

export function topTaskRowsHTML(cols: TableColumn[], rows: TaskRow[]): string {
  if (!rows.length) return noData(cols);
  return rows
    .map((r) => {
      const href = `/tasks/#task-${esc(r.task_id)}`;
      const task = r.title
        ? { html: `<a href="${href}">${esc(r.title)}</a>`, sub: r.task_id }
        : { html: `<a class="mono" href="${href}">${esc(r.task_id)}</a>` };
      return tableRowHTML(
        cols,
        {
          cells: [
            task,
            r.sessions,
            r.turns,
            fmtNum(r.total_tokens),
            fmtDur(r.total_duration_ms),
            { html: costHTML(r.cost_usd, r.estimated_cost_usd, fmtUSD, r.unpriced_sessions) },
          ],
        },
        LOOK,
      );
    })
    .join("");
}
