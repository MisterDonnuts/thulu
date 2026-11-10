import { t } from "../i18n";
import { fmtDate } from "./time";
import { esc } from "./ui";

export interface RetentionSetting {
  key: string;
  value: number;
  default: number;
}

export interface RetentionView {
  windows: RetentionSetting[];
  disk_floor: RetentionSetting;
  last_sweep?: string;
  last_sweep_pressure?: boolean;
  sweep_every_sec?: number;
  disk_free_percent?: number;
}

const WINDOW_LABEL: Record<string, string> = {
  "retention.run_logs_days": t("lib.retention.windows.run_logs"),
  "retention.sessions_days": t("lib.retention.windows.sessions"),
  "retention.webhook_deliveries_days": t("lib.retention.windows.webhook_deliveries"),
  "retention.run_keys_days": t("lib.retention.windows.run_keys"),
  "retention.sandbox_rows_days": t("lib.retention.windows.sandbox_rows"),
  "retention.cache_metrics_days": t("lib.retention.windows.cache_metrics"),
  "retention.stuck_worktree_days": t("lib.retention.windows.stuck_worktree"),
  "audit.retention_days": t("lib.retention.windows.audit"),
};

export function windowLabel(key: string): string {
  return WINDOW_LABEL[key] ?? key;
}

export function daysText(days: number): string {
  return days > 0 ? t("lib.retention.days", { count: days }) : t("lib.retention.forever");
}

export function defaultText(s: RetentionSetting): string {
  return t("lib.retention.default", { value: daysText(s.default) });
}

export function parseDays(raw: string, def: number): number | null {
  const s = raw.trim();
  if (s === "") return def;
  if (!/^\d+$/.test(s)) return null;
  return Number(s);
}

export const DISK_FLOOR_KEY = "janitor.disk_free_min_percent";

export type RetentionProblem = "days" | "percent";

export interface RetentionInput {
  key: string;
  raw: string;
  default: number;
  value: number;
}

export function checkRetention(
  rows: RetentionInput[],
):
  | { ok: true; changes: Record<string, number> }
  | { ok: false; problems: Record<string, RetentionProblem> } {
  const changes: Record<string, number> = {};
  const problems: Record<string, RetentionProblem> = {};
  for (const row of rows) {
    const percent = row.key === DISK_FLOOR_KEY;
    const next = parseDays(row.raw, row.default);
    if (next === null || (percent && next > 100)) {
      problems[row.key] = percent ? "percent" : "days";
      continue;
    }
    if (next !== row.value) changes[row.key] = next;
  }
  return Object.keys(problems).length > 0 ? { ok: false, problems } : { ok: true, changes };
}

export function retentionProblemText(p: RetentionProblem): string {
  return p === "percent" ? t("lib.retention.errors.percent") : t("lib.retention.errors.days");
}

export function refusedRetentionKey(body: unknown, keys: string[]): string | null {
  if (!body || typeof body !== "object") return null;
  const setting = (body as { setting?: unknown }).setting;
  return typeof setting === "string" && keys.includes(setting) ? setting : null;
}

export function sweepLine(v: RetentionView): string {
  const hours = v.sweep_every_sec ? Math.round(v.sweep_every_sec / 3600) : 0;
  const every = hours > 0 ? t("lib.retention.sweep.every", { count: hours }) : "";
  if (!v.last_sweep) return [t("lib.retention.sweep.never"), every].filter(Boolean).join(" ");
  const last = v.last_sweep_pressure
    ? t("lib.retention.sweep.last_pressure", { date: fmtDate(v.last_sweep) })
    : t("lib.retention.sweep.last", { date: fmtDate(v.last_sweep) });
  return [last, every].filter(Boolean).join(" ");
}

export function diskLine(v: RetentionView): string {
  const floor = v.disk_floor.value;
  const free = v.disk_free_percent;
  if (floor <= 0) return t("lib.retention.disk.off");
  if (free === undefined) return t("lib.retention.disk.floor_unknown", { floor });
  return t("lib.retention.disk.floor", { floor, free: Math.floor(free) });
}

export function diskLow(v: RetentionView): boolean {
  return (
    v.disk_floor.value > 0 &&
    v.disk_free_percent !== undefined &&
    v.disk_free_percent < v.disk_floor.value
  );
}

function rowHTML(s: RetentionSetting, label: string, unit: string): string {
  const id = `ret-${s.key.replace(/[^a-z0-9]/gi, "-")}`;
  const max = s.key === DISK_FLOOR_KEY ? ' max="100"' : "";
  return `<div class="ret-row"><label for="${esc(id)}">${esc(label)}</label><span class="ret-field"><input id="${esc(id)}" class="ret-input" type="number" min="0"${max} step="1" inputmode="numeric" value="${s.value}" data-key="${esc(s.key)}" data-default="${s.default}" data-value="${s.value}" aria-describedby="${esc(id)}-error" /> <span class="muted">${esc(unit)}</span></span><span class="muted ret-default">${esc(s.key === DISK_FLOOR_KEY ? t("lib.retention.default", { value: s.default > 0 ? `${s.default}%` : t("lib.retention.disk.off_short") }) : defaultText(s))}</span><span id="${esc(id)}-error" class="field-error ret-row-error" hidden></span></div>`;
}

export function retentionRowsHTML(v: RetentionView): string {
  const rows = v.windows.map((s) => rowHTML(s, windowLabel(s.key), t("lib.retention.unit_days")));
  rows.push(rowHTML(v.disk_floor, t("lib.retention.disk.label"), "%"));
  return rows.join("");
}
