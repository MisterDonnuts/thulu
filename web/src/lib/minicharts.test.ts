import { describe, expect, it } from "vitest";
import {
  flowBars,
  flowChartSVG,
  flowLegend,
  flowSummary,
  hasFlowData,
  type FlowPoint,
} from "./minicharts";

const flow: FlowPoint[] = [
  { bucket: "2026-07-01", created: 4, completed: 2 },
  { bucket: "2026-07-02", created: 0, completed: 3 },
];

describe("hasFlowData", () => {
  it("is false for an empty and for an all-zero window", () => {
    expect(hasFlowData([])).toBe(false);
    expect(hasFlowData([{ bucket: "x", created: 0, completed: 0 }])).toBe(false);
  });
  it("is true as soon as one bucket moved", () => {
    expect(hasFlowData(flow)).toBe(true);
  });
});

describe("flowBars", () => {
  it("puts created before completed and titles each day with both counts", () => {
    const [first, second] = flowBars(flow);
    expect(first.parts.map((p) => p.value)).toEqual([4, 2]);
    expect(first.title).toContain("4 created");
    expect(first.title).toContain("2 completed");
    expect(second.title).toContain("0 created");
  });
  it("labels a day by its UTC date, whatever the reader's zone", () => {
    expect(flowBars(flow)[0].label).toMatch(/Jul\s+01/);
  });
});

describe("flowChartSVG", () => {
  it("draws nothing when nothing moved", () => {
    expect(
      flowChartSVG([{ bucket: "x", created: 0, completed: 0 }], { width: 640, height: 220 }),
    ).toBe("");
  });
  it("draws the grouped bars at the frame's size, a zero part left out", () => {
    const svg = flowChartSVG(flow, { width: 500, height: 220 });
    expect(svg).toContain('viewBox="0 0 500 220"');
    expect(svg.match(/<g class="chart-bar">/g)).toHaveLength(2);
    expect(svg.match(/fill="var\(--viz-/g)).toHaveLength(3);
  });
  it("marks the y axis in whole tasks", () => {
    const svg = flowChartSVG([{ bucket: "2026-07-01", created: 3, completed: 1 }], {
      width: 500,
      height: 220,
    });
    const ticks = [...svg.matchAll(/<text class="chart-tick" x="30.0"[^>]*>([^<]*)</g)].map(
      (m) => m[1],
    );
    expect(ticks).toHaveLength(3);
    for (const tick of ticks) expect(tick).toMatch(/^\d+$/);
    expect(ticks.at(-1)).toBe("4");
  });
});

describe("flowLegend and flowSummary", () => {
  it("keys the two series in the order the bars stand", () => {
    expect(flowLegend().map((i) => [i.label, i.swatch])).toEqual([
      ["created", "square"],
      ["completed", "square"],
    ]);
  });
  it("sums the window for a screen reader", () => {
    expect(flowSummary(flow)).toBe("Tasks over 2 days: 4 created, 5 completed");
  });
});
