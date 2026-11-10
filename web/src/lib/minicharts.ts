import { t } from "../i18n";
import { barChart, type ChartBar, type ChartLegendItem } from "./ui/chart";
import { fmtUsageDay, type UsageBucket } from "./usage-chart";

export interface FlowPoint {
  bucket: string;
  created: number;
  completed: number;
}

export type SpendPoint = UsageBucket;

export function hasFlowData(points: FlowPoint[]): boolean {
  return points.some((p) => p.created > 0 || p.completed > 0);
}

const FLOW_COLORS = { created: "var(--viz-8)", completed: "var(--viz-1)" };

function flowDay(bucket: string): string {
  const ms = Date.parse(bucket);
  return Number.isFinite(ms) ? fmtUsageDay(ms) : bucket;
}

export function flowBars(points: FlowPoint[]): ChartBar[] {
  return points.map((p) => {
    const date = flowDay(p.bucket);
    return {
      label: date,
      title: t("lib.minicharts.throughput.bar_title", {
        date,
        created: t("lib.minicharts.throughput.created_count", { count: p.created }),
        completed: t("lib.minicharts.throughput.completed_count", { count: p.completed }),
      }),
      parts: [
        { value: p.created, color: FLOW_COLORS.created },
        { value: p.completed, color: FLOW_COLORS.completed },
      ],
    };
  });
}

export function flowChartSVG(points: FlowPoint[], size: { width: number; height: number }): string {
  if (!hasFlowData(points)) return "";
  return barChart(flowBars(points), { ...size, layout: "grouped", counts: true });
}

export function flowLegend(): ChartLegendItem[] {
  return [
    {
      label: t("lib.minicharts.throughput.created"),
      color: FLOW_COLORS.created,
      swatch: "square",
    },
    {
      label: t("lib.minicharts.throughput.completed"),
      color: FLOW_COLORS.completed,
      swatch: "square",
    },
  ];
}

export function flowSummary(points: FlowPoint[]): string {
  const created = points.reduce((a, p) => a + p.created, 0);
  const completed = points.reduce((a, p) => a + p.completed, 0);
  return t("lib.minicharts.throughput.summary", {
    count: points.length,
    created: t("lib.minicharts.throughput.created_count", { count: created }),
    completed: t("lib.minicharts.throughput.completed_count", { count: completed }),
  });
}
