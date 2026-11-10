// Shapes mirror GET /infra/cache in internal/sandbox/cache_report.go; change both together.

import { locale, t } from "../i18n";
import { fmtBytes, fmtPercent, workspaceLabel } from "./infra";
import { formatSeconds } from "./time";
import { stackedBarChart, type ChartLegendItem } from "./ui/chart";
import type { TableColumn } from "./ui/table";

export type CacheRange = "7d" | "30d" | "90d";
export const CACHE_RANGES: CacheRange[] = ["7d", "30d", "90d"];

export type SetupStatus = "skipped" | "restored" | "ran" | "failed" | "none";

export interface CacheCounts {
  runs: number;
  with_setup: number;
  hits: number;
  hit_rate: number | null;
  statuses: Partial<Record<SetupStatus, number>>;
  tool_downloads: number;
  prewarms: number;
  prewarms_failed: number;
}

export interface CacheTotals extends CacheCounts {
  no_cache: number;
  runs_downloading: number;
  avg_ms: Partial<Record<SetupStatus, number>>;
  avg_ran_setup_ms: number;
  time_saved_ms: number;
  cache_bytes: number;
  fullest_pct: number | null;
  fullest_workspace?: string;
}

export interface CacheDay {
  date: string;
  skipped: number;
  restored: number;
  ran: number;
  failed: number;
  none: number;
  prewarms: number;
  prewarms_failed: number;
  tool_downloads: number;
}

export interface CacheWorkspace extends CacheCounts {
  workspace_id: string;
  workspace_name?: string;
  measured_at: string | null;
  total_bytes: number;
  capacity_bytes: number;
  used_bytes: number;
  capacity_pct: number | null;
  areas: Record<string, number> | null;
  first_measured_at: string | null;
  growth_bytes: number;
  growth_pct: number | null;
}

export interface CacheProject extends CacheCounts {
  project_id: string;
  project_name?: string;
  workspace_id: string;
  workspace_name?: string;
  last_status?: SetupStatus;
  last_run_at: string | null;
  avg_setup_ms: number;
  snapshot_bytes: number;
  snapshot_count: number;
  snapshot_newest?: string;
  distinct_keys: number;
  recent_keys: number;
  last_prewarm: { status: string; error?: string; at: string } | null;
  prewarm_fail_streak: number;
}

export interface CacheToolRow {
  tool: string;
  version: string;
  bytes: number;
  workspaces: string[] | null;
  last_used: string | null;
  unused: boolean;
}

export type SignalKind =
  | "cache_growth"
  | "cache_full"
  | "low_hit_rate"
  | "prewarm_failing"
  | "key_churn"
  | "tool_unused";

export interface CacheSignal {
  kind: SignalKind;
  workspace_id?: string;
  project_id?: string;
  tool?: string;
  value: number;
  threshold: number;
}

export interface CacheThresholds {
  growth_pct: number;
  capacity_pct: number;
  low_hit_rate: number;
  low_hit_min_runs: number;
  prewarm_failures: number;
  key_churn_keys: number;
  key_churn_days: number;
  tool_unused_days: number;
}

export interface CacheReport {
  range: CacheRange;
  since: string;
  generated_at: string;
  log_since: string | null;
  totals: CacheTotals;
  daily: CacheDay[] | null;
  workspaces: CacheWorkspace[] | null;
  projects: CacheProject[] | null;
  tools: CacheToolRow[] | null;
  signals: CacheSignal[] | null;
  thresholds: CacheThresholds;
}

export function fmtHitRate(rate: number | null | undefined): string {
  return rate === null || rate === undefined ? "—" : fmtPercent(rate * 100);
}

export function fmtDuration(ms: number): string {
  return formatSeconds(Math.max(0, Math.round(ms / 1000)));
}

export function isEmptyReport(r: CacheReport): boolean {
  return (
    r.totals.runs === 0 &&
    r.totals.prewarms === 0 &&
    !(r.workspaces ?? []).length &&
    !(r.projects ?? []).length &&
    !(r.tools ?? []).length
  );
}

export function partialSince(r: CacheReport): string | null {
  if (!r.log_since) return null;
  return Date.parse(r.log_since) > Date.parse(r.since) ? r.log_since : null;
}

export const STACK: Array<Exclude<SetupStatus, "none">> = ["skipped", "restored", "ran", "failed"];

export const STATUS_COLOR: Record<Exclude<SetupStatus, "none">, string> = {
  skipped: "var(--ok)",
  restored: "var(--accent)",
  ran: "var(--warn)",
  failed: "var(--danger)",
};

export function statusLabel(s: SetupStatus): string {
  switch (s) {
    case "skipped":
      return t("lib.infra.cache.status.skipped");
    case "restored":
      return t("lib.infra.cache.status.restored");
    case "ran":
      return t("lib.infra.cache.status.ran");
    case "failed":
      return t("lib.infra.cache.status.failed");
    case "none":
      return t("lib.infra.cache.status.none");
  }
}

function dayLabel(date: string, withYear = false): string {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString(locale, {
    month: "short",
    day: "numeric",
    year: withYear ? "numeric" : undefined,
    timeZone: "UTC",
  });
}

export function dayTitle(d: CacheDay): string {
  return t("lib.infra.cache.bar_title", {
    date: dayLabel(d.date, true),
    skipped: d.skipped,
    restored: d.restored,
    ran: d.ran,
    failed: d.failed,
    prewarms: d.prewarms,
  });
}

export interface BarsOpts {
  width: number;
  height: number;
}

export function stackedBars(days: CacheDay[], o: BarsOpts): string {
  return stackedBarChart(
    days.map((d) => ({
      label: dayLabel(d.date),
      title: dayTitle(d),
      parts: STACK.map((s) => ({ value: d[s], color: STATUS_COLOR[s] })),
    })),
    { ...o, counts: true },
  );
}

export function stackLegend(): ChartLegendItem[] {
  return STACK.map((s) => ({ label: statusLabel(s), color: STATUS_COLOR[s], swatch: "square" }));
}

export function barsSummary(days: CacheDay[]): string {
  const sum = (s: Exclude<SetupStatus, "none">) => days.reduce((a, d) => a + d[s], 0);
  return t("lib.infra.cache.bars_summary", {
    days: days.length,
    skipped: sum("skipped"),
    restored: sum("restored"),
    ran: sum("ran"),
    failed: sum("failed"),
  });
}

export const AREA_ORDER = [
  "npm",
  "go_mod",
  "go_build",
  "pip",
  "mise",
  "snapshots",
  "other",
] as const;
export type Area = (typeof AREA_ORDER)[number];

export function areaLabel(a: string): string {
  switch (a) {
    case "npm":
      return t("lib.infra.cache.areas.npm");
    case "go_mod":
      return t("lib.infra.cache.areas.go_mod");
    case "go_build":
      return t("lib.infra.cache.areas.go_build");
    case "pip":
      return t("lib.infra.cache.areas.pip");
    case "mise":
      return t("lib.infra.cache.areas.mise");
    case "snapshots":
      return t("lib.infra.cache.areas.snapshots");
    default:
      return t("lib.infra.cache.areas.other");
  }
}

export function areaTone(a: string): AreaTone {
  const i = AREA_ORDER.indexOf(a as Area);
  return `viz-${((i < 0 ? AREA_ORDER.length - 1 : i) + 1) as AreaSeries}`;
}
type AreaSeries = 1 | 2 | 3 | 4 | 5 | 6 | 7;
export type AreaTone = `viz-${AreaSeries}`;

export function areaColor(a: string): string {
  return `var(--${areaTone(a)})`;
}

export function areaLegend(seen: ReadonlySet<string>): ChartLegendItem[] {
  return AREA_ORDER.filter((a) => seen.has(a)).map((a) => ({
    label: areaLabel(a),
    color: areaColor(a),
    swatch: "square",
  }));
}

export interface AreaSegment {
  area: string;
  bytes: number;
  pct: number;
}

export function areaSegments(areas: Record<string, number> | null, total: number): AreaSegment[] {
  const by = new Map<string, number>();
  for (const [k, v] of Object.entries(areas ?? {})) {
    const key = (AREA_ORDER as readonly string[]).includes(k) ? k : "other";
    by.set(key, (by.get(key) ?? 0) + v);
  }
  const whole = total > 0 ? total : [...by.values()].reduce((a, b) => a + b, 0);
  return AREA_ORDER.flatMap((a) => {
    const bytes = by.get(a) ?? 0;
    if (bytes <= 0) return [];
    return [{ area: a, bytes, pct: whole > 0 ? Math.min(100, (bytes / whole) * 100) : 0 }];
  });
}

export function fmtGrowth(bytes: number, pct: number | null): string {
  if (pct === null && bytes === 0) return "—";
  const sign = bytes < 0 ? "−" : "+";
  const size = `${sign}${fmtBytes(Math.abs(bytes))}`;
  if (pct === null) return size;
  return `${size} (${pct < 0 ? "−" : "+"}${fmtPercent(Math.abs(pct))})`;
}

export type Tone = "ok" | "warn" | "danger" | "";

export function projectTone(p: CacheProject, th: CacheThresholds): Tone {
  if (p.prewarm_fail_streak >= th.prewarm_failures) return "danger";
  if (p.hit_rate === null) return "";
  if (p.with_setup >= th.low_hit_min_runs && p.hit_rate < th.low_hit_rate) return "warn";
  return "ok";
}

export function sortProjects(list: CacheProject[]): CacheProject[] {
  return [...list].sort(
    (a, b) =>
      b.runs - a.runs ||
      b.snapshot_bytes - a.snapshot_bytes ||
      a.project_id.localeCompare(b.project_id),
  );
}

export interface Names {
  workspace: (id: string) => string;
  project: (id: string) => string;
}

export function namesOf(r: CacheReport): Names {
  const ws = new Map<string, string>();
  const proj = new Map<string, string>();
  for (const w of r.workspaces ?? [])
    if (w.workspace_name) ws.set(w.workspace_id, w.workspace_name);
  for (const p of r.projects ?? []) {
    if (p.project_name) proj.set(p.project_id, p.project_name);
    if (p.workspace_name) ws.set(p.workspace_id, p.workspace_name);
  }
  return {
    workspace: (id) => workspaceLabel(ws.get(id), id),
    project: (id) => proj.get(id) ?? (id || "—"),
  };
}

export function signalText(s: CacheSignal, names: Names, th: CacheThresholds): string {
  const workspace = names.workspace(s.workspace_id ?? "");
  const project = names.project(s.project_id ?? "");
  switch (s.kind) {
    case "cache_growth":
      return t("lib.infra.cache.signals.cache_growth", {
        workspace,
        value: fmtPercent(s.value),
        threshold: fmtPercent(s.threshold),
      });
    case "cache_full":
      return t("lib.infra.cache.signals.cache_full", {
        workspace,
        value: fmtPercent(s.value),
        threshold: fmtPercent(s.threshold),
      });
    case "low_hit_rate":
      return t("lib.infra.cache.signals.low_hit_rate", {
        project,
        workspace,
        value: fmtPercent(s.value),
        threshold: fmtPercent(s.threshold),
      });
    case "prewarm_failing":
      return t("lib.infra.cache.signals.prewarm_failing", { project, workspace, count: s.value });
    case "key_churn":
      return t("lib.infra.cache.signals.key_churn", {
        project,
        workspace,
        count: s.value,
        days: th.key_churn_days,
      });
    case "tool_unused":
      if (s.value < 0) return t("lib.infra.cache.signals.tool_never_used", { tool: s.tool ?? "" });
      return t("lib.infra.cache.signals.tool_unused", { tool: s.tool ?? "", count: s.value });
  }
}

export function signalTone(s: CacheSignal): Tone {
  return s.kind === "cache_full" || s.kind === "prewarm_failing" ? "danger" : "warn";
}

export function projectInitScriptHref(projectID: string): string {
  return `/project/?id=${encodeURIComponent(projectID)}#init-script`;
}

export function cacheProjectColumns(): TableColumn[] {
  return [
    { label: t("infra.cache.projects.columns.project") },
    { label: t("infra.cache.projects.columns.installs"), num: true, width: "shrink" },
    { label: t("infra.cache.projects.columns.hit_rate"), num: true, width: "shrink" },
    { label: t("infra.cache.projects.columns.last") },
    { label: t("infra.cache.projects.columns.full_install"), num: true, width: "shrink" },
    { label: t("infra.cache.projects.columns.snapshots"), num: true },
    { label: t("infra.cache.projects.columns.prewarm") },
  ];
}

export function cacheWorkspaceColumns(): TableColumn[] {
  return [
    { label: t("infra.cache.workspaces.columns.workspace") },
    { label: t("infra.cache.workspaces.columns.size"), num: true, width: "shrink" },
    { label: t("infra.cache.workspaces.columns.areas") },
    { label: t("infra.cache.workspaces.columns.growth"), num: true, width: "shrink" },
    { label: t("infra.cache.workspaces.columns.disk"), num: true, width: "shrink" },
    { label: t("infra.cache.workspaces.columns.measured"), num: true, width: "shrink" },
  ];
}

export function cacheToolColumns(): TableColumn[] {
  return [
    { label: t("infra.cache.tools.columns.tool"), mono: true },
    { label: t("infra.cache.tools.columns.size"), num: true, width: "shrink" },
    { label: t("infra.cache.tools.columns.workspaces") },
    { label: t("infra.cache.tools.columns.last_used"), num: true, width: "shrink" },
  ];
}
