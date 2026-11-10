import { locale, t } from "../i18n";
import { unitShort } from "./time";
import {
  chartBarsHTML,
  chartLegendHTML,
  chartLegendItemsHTML,
  chartPanelClass,
  emptyStateHTML,
  fluidBandsSVG,
  fluidChartHTML,
  fluidLinesSVG,
  niceMax,
  panelHTML,
  sparklineSVG,
  stackTotals,
  statTileHTML,
  tableHTML,
  type Attrs,
  type FluidSeries,
} from "./ui";

export { niceMax };

// Mirrors envmetrics.SeriesData.
export interface SeriesData {
  label: string;
  // Aligned by position with PanelData.buckets; a short series reads undefined past its end, which
  // must draw a gap like null, not NaN.
  points?: Array<number | null | undefined>;
  value: number;
  previous?: number | null;
}

// Mirrors envmetrics.PanelData.
export interface PanelData {
  panel_id: string;
  type: string;
  title: string;
  unit?: string;
  from: string;
  to: string;
  time_axis: boolean;
  width?: number;
  buckets?: string[];
  series: SeriesData[];
  note?: string;
}

const PALETTE = 8;

function paletteSlot(i: number): 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 {
  return ((i % PALETTE) + 1) as 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8;
}

export function seriesColor(i: number): string {
  return `var(--viz-${paletteSlot(i)})`;
}

export function fmtValue(v: number, unit?: string): string {
  if (!Number.isFinite(v)) return "—";
  const abs = Math.abs(v);
  let out: string;
  if (abs >= 1_000_000_000) out = `${trim(v / 1_000_000_000)}B`;
  else if (abs >= 1_000_000) out = `${trim(v / 1_000_000)}M`;
  else if (abs >= 1_000) out = `${trim(v / 1_000)}k`;
  else if (abs >= 100) out = String(Math.round(v));
  else if (abs >= 1) out = trim(v);
  else out = String(Number(v.toPrecision(2)));
  const label = unit ? unitLabel(unit) : "";
  return label ? `${out} ${label}` : out;
}

function trim(v: number): string {
  return String(Number(v.toFixed(v >= 100 ? 0 : 1)));
}

// OTLP writes dimensionless as "1", which would show as "20 1" on an axis. UCUM annotations like
// "{requests}" name the thing counted and belong in the title.
function unitLabel(unit: string): string {
  if (unit === "1" || unit === "count") return "";
  if (unit.startsWith("{") && unit.endsWith("}")) return "";
  if (unit === "percent") return "%";
  return unit;
}

function maxOf(series: SeriesData[]): number {
  let max = 0;
  for (const s of series) {
    if (s.points) {
      for (const p of s.points) if (p !== null && p !== undefined && p > max) max = p;
    } else if (s.value > max) {
      max = s.value;
    }
  }
  return max;
}

function yTicks(max: number, unit?: string): string[] {
  return [0, 0.5, 1].map((f) => fmtValue(max * f, unit));
}

export function timeAxis(buckets: string[]): string[] {
  if (!buckets.length) return [];
  const first = new Date(buckets[0]);
  const final = new Date(buckets[buckets.length - 1]);
  const spansDays =
    !Number.isNaN(first.getTime()) &&
    !Number.isNaN(final.getTime()) &&
    final.getTime() - first.getTime() > 12 * 3600 * 1000;
  const at = (i: number) => {
    const d = new Date(buckets[i]);
    if (Number.isNaN(d.getTime())) return "";
    const clock = d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
    if (!spansDays) return clock;
    return `${d.toLocaleDateString(locale, { month: "short", day: "numeric" })} ${clock}`;
  };
  return buckets.length === 1 ? [at(0)] : [at(0), at(buckets.length - 1)];
}

function fluidSeries(p: PanelData): FluidSeries[] {
  return p.series.map((s, i) => ({ color: seriesColor(i), points: s.points ?? [] }));
}

function chartLabel(p: PanelData): string {
  return [p.title, rangeLabel(p)].filter(Boolean).join(" · ");
}

function renderLine(p: PanelData): string {
  const n = p.buckets?.length ?? 0;
  const max = niceMax(maxOf(p.series));
  return fluidChartHTML({
    label: chartLabel(p),
    yTicks: yTicks(max, p.unit),
    xTicks: timeAxis(p.buckets ?? []),
    plotSVG: fluidLinesSVG(fluidSeries(p), n, max),
  });
}

function renderArea(p: PanelData): string {
  const n = p.buckets?.length ?? 0;
  const series = fluidSeries(p);
  const max = niceMax(Math.max(0, ...stackTotals(series, n)));
  return fluidChartHTML({
    label: chartLabel(p),
    yTicks: yTicks(max, p.unit),
    xTicks: timeAxis(p.buckets ?? []),
    plotSVG: fluidBandsSVG(series, n, max),
  });
}

const MAX_BARS = 12;

function renderBar(p: PanelData): string {
  const rows = p.series.slice(0, MAX_BARS);
  const max = Math.max(0, ...rows.map((s) => s.value));
  return chartBarsHTML(
    rows.map((s, i) => ({
      label: s.label,
      share: max > 0 ? (s.value / max) * 100 : 0,
      value: fmtValue(s.value, p.unit),
      tone: `viz-${paletteSlot(i)}` as const,
    })),
  );
}

export function statDelta(s: SeriesData): string {
  const prev = s.previous;
  if (prev === null || prev === undefined) return t("lib.charts.delta.no_previous");
  if (prev === 0) return "";
  const pct = ((s.value - prev) / Math.abs(prev)) * 100;
  const arrow = pct >= 0 ? "▲" : "▼";
  return t("lib.charts.delta.vs_previous", { arrow, pct: fmtValue(Math.abs(pct)) });
}

function renderStat(p: PanelData, cls: string | undefined, attrs: Attrs): string {
  // Only called with at least one series: renderPanel sends empty panels to the empty state first.
  const s = p.series[0];
  const sub = [statDelta(s), rangeLabel(p), p.note ?? ""].filter(Boolean).join(" · ");
  return statTileHTML({
    label: p.title,
    value: fmtValue(s.value, p.unit),
    sparkHTML: sparklineSVG(s.points ?? []),
    sub,
    cls,
    attrs,
  });
}

function renderTable(p: PanelData): string {
  return tableHTML(
    [
      { label: t("lib.charts.columns.group"), width: "fill" },
      { label: t("lib.charts.columns.value"), num: true },
    ],
    p.series.map((s, i) => ({
      cells: [
        {
          html: chartLegendItemsHTML([{ label: s.label, color: seriesColor(i), swatch: "square" }]),
        },
        fmtValue(s.value, p.unit),
      ],
    })),
    { compact: true },
  );
}

function legend(p: PanelData): string {
  if (p.type !== "line" && p.type !== "area") return "";
  if (p.series.length <= 1 && p.type !== "area") return "";
  return chartLegendHTML(
    p.series.map((s, i) => ({
      label: s.label,
      color: seriesColor(i),
      swatch: p.type === "line" ? "line" : "square",
      value: fmtValue(s.value, p.unit),
    })),
  );
}

const RENDERERS: Record<string, ((p: PanelData) => string) | undefined> = {
  line: renderLine,
  area: renderArea,
  bar: renderBar,
  stat: (p) => renderStat(p, undefined, {}),
  table: renderTable,
};

// Must match the server's chart catalog; both sides pin it in a test, or a valid panel renders
// blank.
export const CHART_RENDERERS = Object.keys(RENDERERS);

export function panelWidth(p: PanelData): 1 | 2 {
  if (p.width && p.width > 0) return p.width >= 2 ? 2 : 1;
  return p.type === "table" ? 2 : 1;
}

export function renderPanel(p: PanelData): string {
  const cls = chartPanelClass(panelWidth(p) === 2);
  const attrs: Attrs = { "data-panel": p.panel_id };
  if (p.type === "stat" && p.series.length) return renderStat(p, cls, attrs);
  const body = p.series.length
    ? (RENDERERS[p.type]?.(p) ??
      emptyStateHTML({
        size: "sm",
        body: t("lib.charts.empty.unknown_type", { type: p.type }),
      }))
    : emptyStateHTML({ size: "sm", body: t("lib.charts.empty.no_data") });
  return panelHTML({
    as: "h4",
    title: p.title,
    aside: rangeLabel(p),
    bodyHTML: body + legend(p),
    foot: p.note,
    cls,
    attrs,
  });
}

export function rangeLabel(p: PanelData): string {
  const from = new Date(p.from).getTime();
  const to = new Date(p.to).getTime();
  if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) return "";
  const hours = Math.round((to - from) / 3_600_000);
  const span = hours < 48 ? unitShort(hours, "hour") : unitShort(Math.round(hours / 24), "day");
  return t("lib.charts.range.last", { span });
}
