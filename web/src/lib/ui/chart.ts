// chartHTML() and chartLegendHTML() must match Chart.astro and ChartLegend.astro; classes live in
// styles/primitives/chart.css.
// Non-series colors come from .chart-* classes: SVG presentation attributes don't follow a custom
// property's theme switch reliably.

import { locale } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { emptyStateHTML } from "./empty";
import { meterHTML, type ProgressTone } from "./progress";

export type ChartSize = "sm" | "md" | "lg" | "xl" | "auto";

const SIZES = new Set<string>(["sm", "md", "lg", "xl", "auto"]);

export interface ChartLook {
  size?: ChartSize;
  fluid?: boolean;
  cls?: string;
}

export function chartClasses(look: ChartLook = {}): string {
  const { size = "md", fluid = false, cls } = look;
  if (!SIZES.has(size)) throw new Error(`chart: no size "${size}"`); // i18n-ignore a programming error
  if (fluid && size === "auto") throw new Error("chart: a fluid frame needs a height"); // i18n-ignore a programming error
  return ["chart", size !== "md" && `chart-${size}`, fluid && "chart-fluid", cls]
    .filter(Boolean)
    .join(" ");
}

export interface ChartA11yInput {
  label?: string;
  interactive?: boolean;
}

export function chartA11y(input: ChartA11yInput): Attrs {
  return {
    role: "img",
    "aria-label": input.label,
    tabindex: input.interactive ? "0" : undefined,
  };
}

export interface ChartOptions extends ChartLook, ChartA11yInput {
  id?: string;
  bodyHTML?: string;
  attrs?: Attrs;
}

export function chartHTML(o: ChartOptions = {}): string {
  const own: Attrs = {
    id: o.id,
    class: chartClasses({ size: o.size, fluid: o.fluid, cls: o.cls }),
    ...chartA11y(o),
  };
  return `<div${attrsHTML({ ...own, ...o.attrs })}>${o.bodyHTML ?? ""}</div>`;
}

export function chartEmptyHTML(text: string, o: { icon?: string } = {}): string {
  return emptyStateHTML({ size: "sm", dashed: true, icon: o.icon, title: text });
}

export type ChartSwatch = "line" | "square";

export type ChartLegendPlacement = "head" | "body";

const SWATCHES = new Set<string>(["line", "square"]);
const PLACEMENTS = new Set<string>(["head", "body"]);

export interface ChartLegendItem {
  label: string;
  color: string;
  swatch?: ChartSwatch;
  value?: string;
}

export interface ChartLegendLook {
  placement?: ChartLegendPlacement;
  cls?: string;
}

export function chartLegendClasses(look: ChartLegendLook = {}): string {
  const { placement = "body", cls } = look;
  if (!PLACEMENTS.has(placement)) throw new Error(`chart: no legend placement "${placement}"`); // i18n-ignore a programming error
  return ["chart-legend", `chart-legend-${placement}`, cls].filter(Boolean).join(" ");
}

export function chartKeyClasses(swatch: ChartSwatch = "line"): string {
  if (!SWATCHES.has(swatch)) throw new Error(`chart: no swatch "${swatch}"`); // i18n-ignore a programming error
  return swatch === "line" ? "chart-key" : "chart-key chart-key-square";
}

function chartKeyHTML(color: string | undefined, swatch?: ChartSwatch): string {
  const style = color ? ` style="background:${esc(color)}"` : "";
  return `<i class="${chartKeyClasses(swatch)}" aria-hidden="true"${style}></i>`;
}

export interface ChartLegendOptions extends ChartLegendLook {
  id?: string;
  attrs?: Attrs;
}

export function chartLegendHTML(items: ChartLegendItem[], o: ChartLegendOptions = {}): string {
  const own: Attrs = {
    id: o.id,
    class: chartLegendClasses({ placement: o.placement, cls: o.cls }),
  };
  return `<div${attrsHTML({ ...own, ...o.attrs })}>${chartLegendItemsHTML(items)}</div>`;
}

export function chartLegendItemsHTML(items: ChartLegendItem[]): string {
  return items
    .map(
      (it) =>
        `<span class="chart-legend-item">${chartKeyHTML(it.color, it.swatch)}${esc(it.label)}` +
        (it.value !== undefined ? `<span class="chart-legend-value">${esc(it.value)}</span>` : "") +
        `</span>`,
    )
    .join("");
}

export function setChartLegend(legend: HTMLElement, items: ChartLegendItem[]): void {
  const html = chartLegendItemsHTML(items);
  if (legend.innerHTML !== html) legend.innerHTML = html;
}

export interface ChartTipRow {
  value: string;
  label?: string;
  color?: string;
  swatch?: ChartSwatch;
}

export interface ChartTipContent {
  time?: string;
  rows: ChartTipRow[];
}

export function chartTipHTML(c: ChartTipContent): string {
  const time = c.time !== undefined ? `<div class="chart-tip-time">${esc(c.time)}</div>` : "";
  const rows = c.rows
    .map(
      (r) =>
        `<div class="chart-tip-row">` +
        (r.color ? chartKeyHTML(r.color, r.swatch) : "") +
        `<strong class="chart-tip-value">${esc(r.value)}</strong>` +
        (r.label !== undefined ? `<span class="chart-tip-label">${esc(r.label)}</span>` : "") +
        `</div>`,
    )
    .join("");
  return time + rows;
}

export function chartTip(host: HTMLElement): HTMLElement {
  let tip = host.querySelector<HTMLElement>(":scope > .chart-tip");
  if (!tip) {
    tip = document.createElement("div");
    tip.className = "chart-tip";
    tip.hidden = true;
    tip.setAttribute("aria-hidden", "true");
    host.appendChild(tip);
  }
  return tip;
}

export function placeChartTip(tip: HTMLElement, x: number, hostWidth: number): void {
  tip.style.left = `${x}px`;
  tip.classList.toggle("chart-tip-left", x > hostWidth / 2);
}

export function niceMax(max: number): number {
  if (!Number.isFinite(max) || max <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(max)));
  const norm = max / mag;
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10;
  return step * mag;
}

export function countMax(max: number): number {
  return 2 * Math.ceil(niceMax(max / 2));
}

export function nearestIndex(xs: number[], x: number): number {
  let best = -1;
  let bestD = Infinity;
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - x);
    if (d < bestD) {
      best = i;
      bestD = d;
    }
  }
  return best;
}

function gridLine(x0: number, x1: number, y: number): string {
  return `<line class="chart-grid" x1="${x0}" x2="${x1}" y1="${y.toFixed(1)}" y2="${y.toFixed(1)}"/>`;
}

function tick(x: number, y: number, anchor: string, text: string): string {
  return `<text class="chart-tick" x="${x.toFixed(1)}" y="${y.toFixed(1)}" text-anchor="${anchor}">${esc(text)}</text>`;
}

export interface TimePoint {
  t: string;
}

export interface ChartSeries<P extends TimePoint = TimePoint> {
  label: string;
  color: string;
  value: (p: P) => number;
}

export interface TimeChartOpts {
  width: number;
  height: number;
  rangeMs: number;
  end: number;
  intervalMs: number;
  yMax?: number;
  nice?: (max: number) => number;
  fmtY: (v: number) => string;
  fmtX?: (ms: number) => string;
  wash?: boolean;
}

export const CHART_PAD = { l: 64, r: 12, t: 10, b: 22 };

export interface ChartModel {
  svg: string;
  xs: number[];
  ys: number[][];
  width: number;
  height: number;
}

function timeLabel(ms: number): string {
  return new Date(ms).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
}

// A label like "02:36 AM" takes about 50px at the tick size; narrower plots get fewer intervals so
// labels don't collide.
export function timeTicks(plotWidth: number): number {
  if (plotWidth < 300) return 2;
  if (plotWidth < 480) return 3;
  return 4;
}

export function chartWindow(points: TimePoint[], rangeMs: number, minMs = 2 * 60_000): number {
  if (points.length < 2) return rangeMs;
  const span = Date.parse(points[points.length - 1].t) - Date.parse(points[0].t);
  if (!Number.isFinite(span) || span >= rangeMs) return rangeMs;
  return Math.min(rangeMs, Math.max(minMs, span));
}

export function timeChart<P extends TimePoint>(
  points: P[],
  series: Array<ChartSeries<P>>,
  o: TimeChartOpts,
): ChartModel {
  const W = Math.max(120, Math.round(o.width));
  const H = o.height;
  const x0 = CHART_PAD.l;
  const x1 = W - CHART_PAD.r;
  const y0 = H - CHART_PAD.b;
  const y1 = CHART_PAD.t;
  const start = o.end - o.rangeMs;
  const inWindow = points.filter((p) => Date.parse(p.t) >= start);
  const fmtX = o.fmtX ?? timeLabel;
  const wash = o.wash ?? series.length === 1;

  let max = o.yMax ?? 0;
  if (o.yMax === undefined) {
    for (const p of inWindow) for (const s of series) max = Math.max(max, s.value(p));
    max = (o.nice ?? niceMax)(max);
  }
  const xOf = (ms: number) => x0 + (o.rangeMs ? (ms - start) / o.rangeMs : 1) * (x1 - x0);
  const yOf = (v: number) => y0 - (Math.min(v, max) / max) * (y0 - y1);

  const parts: string[] = [];
  for (const f of [0, 0.5, 1]) {
    const y = yOf(max * f);
    parts.push(gridLine(x0, x1, y), tick(x0 - 6, y + 3, "end", o.fmtY(max * f)));
  }
  const ticks = timeTicks(x1 - x0);
  for (let i = 0; i <= ticks; i++) {
    const ms = start + (o.rangeMs * i) / ticks;
    const anchor = i === 0 ? "start" : i === ticks ? "end" : "middle";
    parts.push(tick(xOf(ms), H - 6, anchor, fmtX(ms)));
  }

  const xs = inWindow.map((p) => xOf(Date.parse(p.t)));
  const ys = series.map((s) => inWindow.map((p) => yOf(s.value(p))));
  const breakAfter = inWindow.map(
    (p, i) =>
      i < inWindow.length - 1 && Date.parse(inWindow[i + 1].t) - Date.parse(p.t) > o.intervalMs * 3,
  );

  series.forEach((s, si) => {
    const runs: string[][] = [[]];
    inWindow.forEach((_, i) => {
      runs[runs.length - 1].push(`${xs[i].toFixed(1)},${ys[si][i].toFixed(1)}`);
      if (breakAfter[i]) runs.push([]);
    });
    for (const run of runs) {
      if (run.length < 2) continue;
      if (wash) {
        const first = run[0].split(",")[0];
        const last = run[run.length - 1].split(",")[0];
        parts.push(
          `<path class="chart-wash" fill="${esc(s.color)}" d="M${first},${y0} L${run.join(" L")} L${last},${y0} Z"/>`,
        );
      }
      parts.push(
        `<polyline class="chart-line" stroke="${esc(s.color)}" points="${run.join(" ")}"/>`,
      );
    }
  });
  parts.push(`<line class="chart-cross" x1="0" x2="0" y1="${y1}" y2="${y0}" visibility="hidden"/>`);
  series.forEach((s, si) =>
    parts.push(
      `<circle class="chart-dot" data-series="${si}" r="4" fill="${esc(s.color)}" visibility="hidden"/>`,
    ),
  );

  return {
    svg: `<svg viewBox="0 0 ${W} ${H}" width="${W}" height="${H}" role="img" aria-hidden="true">${parts.join("")}</svg>`,
    xs,
    ys,
    width: W,
    height: H,
  };
}

export interface CrosshairOpts {
  fmtTime?: (ms: number) => string;
}

export function bindCrosshair<P extends TimePoint>(
  host: HTMLElement,
  model: ChartModel,
  points: P[],
  series: Array<ChartSeries<P>>,
  fmt: (v: number) => string,
  o: CrosshairOpts = {},
): () => void {
  const svg = host.querySelector("svg");
  const cross = host.querySelector<SVGLineElement>(".chart-cross");
  const dots = Array.from(host.querySelectorAll<SVGCircleElement>(".chart-dot"));
  if (!svg || !cross || !model.xs.length) return () => undefined;
  const offset = points.length - model.xs.length; // timeChart drops points before the window, so model.xs[i] is points[i + offset].
  const tip = chartTip(host);
  const fmtTime = o.fmtTime ?? ((ms: number) => new Date(ms).toLocaleTimeString(locale));
  let current = -1;

  const show = (i: number) => {
    if (i < 0) return hide();
    current = i;
    const x = model.xs[i];
    cross.setAttribute("x1", String(x));
    cross.setAttribute("x2", String(x));
    cross.setAttribute("visibility", "visible");
    dots.forEach((d, si) => {
      d.setAttribute("cx", String(x));
      d.setAttribute("cy", String(model.ys[si][i]));
      d.setAttribute("visibility", "visible");
    });
    const p = points[i + offset];
    tip.innerHTML = chartTipHTML({
      time: fmtTime(Date.parse(p.t)),
      rows: series.map((s) => ({ value: fmt(s.value(p)), label: s.label, color: s.color })),
    });
    tip.hidden = false;
    const scale = svg.getBoundingClientRect().width / model.width || 1;
    placeChartTip(tip, x * scale, host.clientWidth);
  };
  const hide = () => {
    current = -1;
    cross.setAttribute("visibility", "hidden");
    dots.forEach((d) => d.setAttribute("visibility", "hidden"));
    tip.hidden = true;
  };
  const onMove = (e: PointerEvent) => {
    const r = svg.getBoundingClientRect();
    const x = ((e.clientX - r.left) / r.width) * model.width;
    show(nearestIndex(model.xs, x));
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape" && current >= 0) {
      hide();
      return;
    }
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight" && e.key !== "Home" && e.key !== "End")
      return;
    e.preventDefault();
    const last = model.xs.length - 1;
    if (e.key === "Home") show(0);
    else if (e.key === "End") show(last);
    else if (current < 0) show(last);
    else show(Math.min(last, Math.max(0, current + (e.key === "ArrowLeft" ? -1 : 1))));
  };
  host.addEventListener("pointermove", onMove);
  host.addEventListener("pointerleave", hide);
  host.addEventListener("keydown", onKey);
  host.addEventListener("blur", hide);
  return () => {
    host.removeEventListener("pointermove", onMove);
    host.removeEventListener("pointerleave", hide);
    host.removeEventListener("keydown", onKey);
    host.removeEventListener("blur", hide);
    tip.hidden = true;
  };
}

export interface ChartBarPart {
  value: number;
  color: string;
}

export interface ChartBar {
  label: string;
  title: string;
  parts: ChartBarPart[];
}

export type ChartBarLayout = "stacked" | "grouped";

const BAR_LAYOUTS = new Set<string>(["stacked", "grouped"]);

export interface BarChartOpts {
  width: number;
  height: number;
  layout?: ChartBarLayout;
  fmtY?: (v: number) => string;
  counts?: boolean;
}

export type StackedBarOpts = Omit<BarChartOpts, "layout">;

const BAR_PAD = { l: 36, r: 8, t: 8, b: 22 };

const GROUP_GAP = 1;

export function barChart(bars: ChartBar[], o: BarChartOpts): string {
  const layout = o.layout ?? "stacked";
  if (!BAR_LAYOUTS.has(layout)) throw new Error(`chart: no bar layout "${layout}"`); // i18n-ignore a programming error
  const grouped = layout === "grouped";
  const W = Math.max(160, Math.round(o.width));
  const H = o.height;
  const x0 = BAR_PAD.l;
  const x1 = W - BAR_PAD.r;
  const y0 = H - BAR_PAD.b;
  const y1 = BAR_PAD.t;
  const height = (b: ChartBar) =>
    grouped
      ? Math.max(0, ...b.parts.map((p) => p.value))
      : b.parts.reduce((a, p) => a + p.value, 0);
  const tallest = Math.max(1, ...bars.map(height));
  const max = o.counts ? countMax(tallest) : niceMax(tallest);
  const yOf = (v: number) => y0 - (v / max) * (y0 - y1);
  const slot = bars.length ? (x1 - x0) / bars.length : 0;
  const barW = Math.max(1, Math.min(28, slot * 0.7));
  const fmtY =
    o.fmtY ??
    ((v: number) => v.toLocaleString(locale, { maximumFractionDigits: o.counts ? 0 : 1 }));

  const parts: string[] = [];
  for (const f of [0, 0.5, 1]) {
    const y = yOf(max * f);
    parts.push(gridLine(x0, x1, y), tick(x0 - 6, y + 3, "end", fmtY(max * f)));
  }
  const labelled = new Set([0, Math.floor((bars.length - 1) / 2), bars.length - 1]);
  bars.forEach((b, i) => {
    const cx = x0 + slot * i + slot / 2;
    const left = cx - barW / 2;
    const n = Math.max(1, b.parts.length);
    const partW = grouped ? Math.max(0.5, (barW - GROUP_GAP * (n - 1)) / n) : barW;
    const rects: string[] = [];
    let base = 0;
    b.parts.forEach((part, pi) => {
      if (!part.value) return;
      const top = yOf((grouped ? 0 : base) + part.value);
      const h = yOf(grouped ? 0 : base) - top;
      const x = grouped ? left + pi * (partW + GROUP_GAP) : left;
      rects.push(
        `<rect x="${x.toFixed(1)}" y="${top.toFixed(1)}" width="${partW.toFixed(1)}" height="${h.toFixed(1)}" fill="${esc(part.color)}"/>`,
      );
      base += part.value;
    });
    parts.push(
      `<g class="chart-bar"><title>${esc(b.title)}</title>` +
        `<rect class="chart-hit" x="${(cx - slot / 2).toFixed(1)}" y="${y1}" width="${slot.toFixed(1)}" height="${(y0 - y1).toFixed(1)}"/>` +
        `${rects.join("")}</g>`,
    );
    if (labelled.has(i)) {
      const anchor = i === 0 ? "start" : i === bars.length - 1 ? "end" : "middle";
      const tx = anchor === "start" ? cx - slot / 2 : anchor === "end" ? cx + slot / 2 : cx;
      parts.push(tick(tx, H - 6, anchor, b.label));
    }
  });
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img">${parts.join("")}</svg>`;
}

export function stackedBarChart(bars: ChartBar[], o: StackedBarOpts): string {
  return barChart(bars, { ...o, layout: "stacked" });
}

// Units are shares of a box stretched with preserveAspectRatio none; stroke widths hold only
// because chart.css uses a non-scaling stroke.
export const FLUID_PLOT = { w: 1000, h: 100 };

export interface FluidSeries {
  color: string;
  points: Array<number | null | undefined>;
}

export function chartRuns(
  points: Array<number | null | undefined>,
): Array<Array<{ i: number; v: number }>> {
  const runs: Array<Array<{ i: number; v: number }>> = [];
  let cur: Array<{ i: number; v: number }> = [];
  points.forEach((p, i) => {
    if (p === null || p === undefined || !Number.isFinite(p)) {
      if (cur.length) runs.push(cur);
      cur = [];
      return;
    }
    cur.push({ i, v: p });
  });
  if (cur.length) runs.push(cur);
  return runs;
}

function round1(v: number): string {
  return String(Math.round(v * 10) / 10);
}

function fluidX(i: number, n: number): number {
  return n <= 1 ? FLUID_PLOT.w / 2 : (i / (n - 1)) * FLUID_PLOT.w;
}

function fluidY(v: number, max: number): number {
  return FLUID_PLOT.h - (max > 0 ? (Math.min(v, max) / max) * FLUID_PLOT.h : 0);
}

export function fluidLinesSVG(series: FluidSeries[], n: number, max: number): string {
  return series
    .map((s) =>
      chartRuns(s.points)
        .map((run) => {
          const pts = run.map((p) => `${round1(fluidX(p.i, n))},${round1(fluidY(p.v, max))}`);
          if (pts.length === 1) {
            return `<polyline class="chart-line chart-point" stroke="${esc(s.color)}" points="${pts[0]} ${pts[0]}"/>`;
          }
          return `<polyline class="chart-line" stroke="${esc(s.color)}" points="${pts.join(" ")}"/>`;
        })
        .join(""),
    )
    .join("");
}

export function fluidBandsSVG(series: FluidSeries[], n: number, max: number): string {
  const base = new Array<number>(n).fill(0);
  return series
    .map((s) => {
      const top = base.map((b, i) => b + (s.points[i] ?? 0));
      const up = top.map((v, i) => `${round1(fluidX(i, n))},${round1(fluidY(v, max))}`);
      const down = base
        .map((v, i) => `${round1(fluidX(i, n))},${round1(fluidY(v, max))}`)
        .reverse();
      for (let i = 0; i < n; i++) base[i] = top[i];
      return `<polygon class="chart-band" fill="${esc(s.color)}" points="${up.concat(down).join(" ")}"/>`;
    })
    .join("");
}

export function stackTotals(series: FluidSeries[], n: number): number[] {
  const out = new Array<number>(n).fill(0);
  for (const s of series) for (let i = 0; i < n; i++) out[i] += s.points[i] ?? 0;
  return out;
}

export interface FluidChartOptions extends Omit<ChartOptions, "bodyHTML" | "fluid"> {
  yTicks: string[];
  xTicks?: string[];
  plotSVG: string;
}

export function fluidChartHTML(o: FluidChartOptions): string {
  const steps = Math.max(1, o.yTicks.length - 1);
  const grid = o.yTicks
    .map((_, i) => {
      const y = round1(FLUID_PLOT.h - (i / steps) * FLUID_PLOT.h);
      return `<line class="chart-grid" x1="0" x2="${FLUID_PLOT.w}" y1="${y}" y2="${y}"/>`;
    })
    .join("");
  const yAxis =
    `<div class="chart-axis chart-axis-y" aria-hidden="true">` +
    [...o.yTicks]
      .reverse()
      .map((l) => `<span class="chart-tick">${esc(l)}</span>`)
      .join("") +
    `</div>`;
  const xAxis = o.xTicks?.length
    ? `<div class="chart-axis chart-axis-x" aria-hidden="true">` +
      o.xTicks.map((l) => `<span class="chart-tick">${esc(l)}</span>`).join("") +
      `</div>`
    : "";
  const plot =
    `<svg class="chart-plot" viewBox="0 0 ${FLUID_PLOT.w} ${FLUID_PLOT.h}" preserveAspectRatio="none" aria-hidden="true">` +
    grid +
    o.plotSVG +
    `</svg>`;
  return chartHTML({
    id: o.id,
    size: o.size,
    cls: o.cls,
    label: o.label,
    interactive: o.interactive,
    attrs: o.attrs,
    fluid: true,
    bodyHTML: yAxis + plot + xAxis,
  });
}

export function chartPanelsHTML(panels: string[]): string {
  return `<div class="chart-panels">${panels.join("")}</div>`;
}

export function chartPanelClass(full: boolean): string | undefined {
  return full ? "chart-panel-full" : undefined;
}

export interface ChartBarRow {
  label: string;
  title?: string;
  share: number;
  value: string;
  valueText?: string;
  sub?: string;
  tone?: ProgressTone;
  mono?: boolean;
}

export interface ChartBarsOptions {
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function chartBarsHTML(rows: ChartBarRow[], o: ChartBarsOptions = {}): string {
  const own: Attrs = { id: o.id, class: ["chart-bars", o.cls].filter(Boolean).join(" ") };
  return `<div${attrsHTML({ ...own, ...o.attrs })}>${rows.map(chartBarRowHTML).join("")}</div>`;
}

export function chartBarRowHTML(r: ChartBarRow): string {
  return (
    `<div class="chart-bars-row">` +
    `<span class="chart-bars-label${r.mono ? " chart-bars-label-mono" : ""}" title="${esc(r.title ?? r.label)}">${esc(r.label)}</span>` +
    meterHTML(r.share, {
      size: "md",
      label: r.label,
      valueText: r.valueText ?? r.value,
      tone: r.tone,
    }) +
    `<span class="chart-bars-value">${esc(r.value)}` +
    (r.sub !== undefined ? `<span class="chart-bars-sub">${esc(r.sub)}</span>` : "") +
    `</span></div>`
  );
}

export interface SparklineOpts {
  width?: number;
  height?: number;
}

export function sparklineSVG(
  values: Array<number | null | undefined>,
  o: SparklineOpts = {},
): string {
  const seen = values.filter((v): v is number => typeof v === "number" && Number.isFinite(v));
  if (seen.length < 2) return "";
  const w = o.width ?? 68;
  const h = o.height ?? 18;
  const min = Math.min(...seen);
  const max = Math.max(...seen);
  const span = max - min || 1;
  const runs = chartRuns(values).filter((run) => run.length > 1);
  if (!runs.length) return "";
  const lines = runs
    .map(
      (run) =>
        `<polyline class="chart-spark-line" points="${run
          .map((p) => `${(p.i / (values.length - 1)) * w},${h - ((p.v - min) / span) * h}`)
          .join(" ")}"/>`,
    )
    .join("");
  return `<svg class="chart-spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" aria-hidden="true">${lines}</svg>`;
}
