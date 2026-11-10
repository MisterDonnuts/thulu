import { locale, t } from "../i18n";
import type { ChartSeries, TimePoint } from "./ui/chart";

export interface UsageBucket {
  bucket: string;
  cost_usd: number;
  total_tokens: number;
}

export interface UsageDay extends TimePoint {
  cost: number;
  tokens: number;
}

export type UsageMetric = "cost" | "tokens";

export const DAY_MS = 86_400_000;

// Must match the daemon's default window (Filter.Defaults).
export const DEFAULT_WINDOW_MS = 30 * DAY_MS;

function dayStart(ms: number): number {
  return Math.floor(ms / DAY_MS) * DAY_MS;
}

// The daemon omits days with no session; fill them with zeros or a quiet day draws as a slope
// between busy ones.
export function usageDays(
  buckets: UsageBucket[],
  window: { from?: string; to?: string } = {},
  now: number = Date.now(),
): UsageDay[] {
  const byDay = new Map<number, UsageBucket>();
  for (const b of buckets) {
    const ms = Date.parse(b.bucket);
    if (Number.isFinite(ms)) byDay.set(dayStart(ms), b);
  }
  if (!byDay.size) return [];
  const to = window.to ? Date.parse(window.to) : now;
  const from = window.from ? Date.parse(window.from) : to - DEFAULT_WINDOW_MS;
  const seen = [...byDay.keys()];
  const first = Math.min(dayStart(Number.isFinite(from) ? from : Math.min(...seen)), ...seen);
  const last = Math.max(dayStart(Number.isFinite(to) ? to : Math.max(...seen)), ...seen);
  const days: UsageDay[] = [];
  for (let d = first; d <= last; d += DAY_MS) {
    const b = byDay.get(d);
    days.push({
      t: new Date(d).toISOString(),
      cost: b?.cost_usd ?? 0,
      tokens: b?.total_tokens ?? 0,
    });
  }
  return days;
}

export function usageSeries(metric: UsageMetric): ChartSeries<UsageDay> {
  return metric === "cost"
    ? { label: t("usage.chart.cost"), color: "var(--viz-1)", value: (d) => d.cost }
    : { label: t("usage.chart.tokens"), color: "var(--viz-1)", value: (d) => d.tokens };
}

// The days are UTC midnights; formatting in local time would shift them a day.
export function fmtUsageDay(ms: number): string {
  return new Date(ms).toLocaleDateString(locale, {
    month: "short",
    day: "2-digit",
    timeZone: "UTC",
  });
}

export function usageChartSummary(
  days: UsageDay[],
  metric: UsageMetric,
  fmt: (v: number) => string,
): string {
  const s = usageSeries(metric);
  const total = days.reduce((a, d) => a + s.value(d), 0);
  return t("lib.usage_chart.summary", { metric: s.label, count: days.length, total: fmt(total) });
}
