import { describe, expect, it } from "vitest";

import {
  CHART_RENDERERS,
  fmtValue,
  niceMax,
  panelWidth,
  rangeLabel,
  renderPanel,
  statDelta,
  timeAxis,
  type PanelData,
} from "./charts";

function panel(over: Partial<PanelData> = {}): PanelData {
  const buckets = ["2026-08-17T10:00:00Z", "2026-08-17T10:05:00Z", "2026-08-17T10:10:00Z"];
  return {
    panel_id: "p1",
    type: "line",
    title: "Errors",
    from: "2026-08-17T10:00:00Z",
    to: "2026-08-17T10:15:00Z",
    time_axis: true,
    buckets,
    series: [{ label: "visa", points: [1, 2, 3], value: 6 }],
    ...over,
  };
}

describe("the renderer catalog", () => {
  it("covers exactly the server's chart types", () => {
    expect([...CHART_RENDERERS].sort()).toEqual(["area", "bar", "line", "stat", "table"]);
  });
});

describe("fmtValue", () => {
  it("abbreviates thousands so an axis label fits", () => {
    expect(fmtValue(1240)).toBe("1.2k");
    expect(fmtValue(2_500_000)).toBe("2.5M");
  });

  it("keeps small numbers readable rather than rounding them to zero", () => {
    expect(fmtValue(0.42)).toBe("0.42");
  });

  it("renders percent as a suffix and a real unit as itself", () => {
    expect(fmtValue(42, "percent")).toBe("42 %");
    expect(fmtValue(120, "ms")).toBe("120 ms");
  });

  it("prints no unit at all for the three spellings of dimensionless", () => {
    expect(fmtValue(42, "1")).toBe("42");
    expect(fmtValue(42, "count")).toBe("42");
    expect(fmtValue(42, "{requests}")).toBe("42");
  });
});

describe("niceMax", () => {
  it("rounds an axis up to a number people read", () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(7)).toBe(10);
    expect(niceMax(23)).toBe(50);
    expect(niceMax(120)).toBe(200);
  });
});

describe("renderPanel", () => {
  it("draws one polyline per unbroken run", () => {
    const html = renderPanel(
      panel({ series: [{ label: "visa", points: [1, null, 3, 4], value: 8 }] }),
    );
    expect((html.match(/<polyline class="chart-line"/g) ?? []).length).toBe(1);
    expect((html.match(/<polyline class="chart-line chart-point"/g) ?? []).length).toBe(1);
  });

  it("escapes label values, which come straight off the wire", () => {
    const html = renderPanel(
      panel({
        type: "bar",
        time_axis: false,
        series: [{ label: `<img src=x onerror="alert(1)">`, value: 3 }],
      }),
    );
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
  });

  it("lists bars by name, the longest full and no legend, since every row names itself", () => {
    const html = renderPanel(
      panel({
        type: "bar",
        time_axis: false,
        series: [
          { label: "visa", value: 300 },
          { label: "amex", value: 150 },
        ],
      }),
    );
    expect(html.match(/class="chart-bars-row"/g)).toHaveLength(2);
    expect(html).toContain("width:100%");
    expect(html).toContain("width:50%");
    expect(html).toContain('data-tone="viz-2"');
    expect(html).not.toContain("chart-legend");
  });

  it("draws a table with a colour key per group and the figures on the right", () => {
    const html = renderPanel(
      panel({
        type: "table",
        series: [
          { label: "visa", value: 1240 },
          { label: "amex", value: 3 },
        ],
      }),
    );
    expect(html).toContain('class="list-table list-table-compact"');
    expect(html).toContain(
      '<span class="chart-legend-item"><i class="chart-key chart-key-square" aria-hidden="true" style="background:var(--viz-2)"></i>amex</span>',
    );
    expect(html).toContain('<td class="num">1.2k</td>');
  });

  it("colours a group the same in a bar panel and a table panel", () => {
    const series = Array.from({ length: 10 }, (_, i) => ({ label: `g${i}`, value: 10 - i }));
    const bars = renderPanel(panel({ type: "bar", time_axis: false, series }));
    const table = renderPanel(panel({ type: "table", series }));
    const tones = [...bars.matchAll(/data-tone="(viz-\d)"/g)].map((m) => `var(--${m[1]})`);
    const keys = [...table.matchAll(/style="background:(var\(--viz-\d\))"/g)].map((m) => m[1]);
    expect(tones.length).toBeGreaterThan(1);
    expect(tones).toEqual(keys.slice(0, tones.length));
  });

  it("escapes the panel title too", () => {
    const html = renderPanel(panel({ title: `<script>x</script>` }));
    expect(html).not.toContain("<script>");
  });

  it("says when a panel has nothing to draw", () => {
    const html = renderPanel(panel({ series: [] }));
    expect(html).toContain("no data in this window");
    expect(html).toContain('class="empty-state empty-state-sm"');
  });

  it("names an unknown chart type instead of drawing a blank card", () => {
    const html = renderPanel(panel({ type: "sankey" }));
    expect(html).toContain("unknown chart type");
    expect(html).toContain("sankey");
    expect(html).toContain('class="empty-state empty-state-sm"');
  });

  it("surfaces the note about what is not being shown in the panel's foot", () => {
    const html = renderPanel(panel({ note: '4 smaller groups folded into "other"' }));
    expect(html).toContain(
      '<div class="panel-foot">4 smaller groups folded into &quot;other&quot;</div>',
    );
  });

  it("is a panel with its title and window in the head", () => {
    const html = renderPanel(panel());
    expect(html).toMatch(/^<section class="panel" data-panel="p1">/);
    expect(html).toContain('<h4 class="panel-title">Errors</h4>');
    const range = rangeLabel(panel());
    expect(html).toContain(`<div class="panel-head-aside">${range}</div>`);
    expect(html).toContain(`class="chart chart-fluid" role="img" aria-label="Errors · ${range}"`);
  });

  it("shows the stat delta against the previous window", () => {
    const html = renderPanel(
      panel({
        type: "stat",
        series: [{ label: "orders", points: [4, 8, 12], value: 24, previous: 12 }],
      }),
    );
    expect(html).toContain("100");
    expect(html).toContain("vs previous");
    expect(html).toContain("▲");
    expect(html).toMatch(/^<div class="stat" data-panel="p1">/);
    expect(html).toContain('<span class="stat-label">Errors</span>');
    expect(html).toContain('<span class="stat-value">24</span>');
    expect(html).toContain('class="chart-spark"');
    expect(html).toContain(`· ${rangeLabel(panel())}`);
  });

  it("states a delta without an arrow from nothing, and down as down", () => {
    expect(statDelta({ label: "x", value: 5, previous: 0 })).toBe("");
    expect(statDelta({ label: "x", value: 5, previous: 10 })).toContain("▼ 50%");
  });

  it("says so when there is no previous window rather than implying zero change", () => {
    const html = renderPanel(
      panel({ type: "stat", series: [{ label: "orders", points: [1], value: 1 }] }),
    );
    expect(html).toContain("no previous window");
  });

  it("stacks area bands", () => {
    const html = renderPanel(
      panel({
        type: "area",
        series: [
          { label: "visa", points: [1, 1, 1], value: 3 },
          { label: "amex", points: [2, 2, 2], value: 6 },
        ],
      }),
    );
    expect((html.match(/<polygon/g) ?? []).length).toBe(2);
  });

  it("puts a legend on a multi-series chart and none on a single one", () => {
    expect(renderPanel(panel())).not.toContain("chart-legend");
    const two = renderPanel(
      panel({
        series: [
          { label: "visa", points: [1, 2, 3], value: 6 },
          { label: "amex", points: [3, 2, 1], value: 6 },
        ],
      }),
    );
    expect(two).toContain('class="chart-legend chart-legend-body"');
    expect(two.match(/class="chart-legend-item"/g)?.length).toBe(2);
    expect(two).toContain(
      '<i class="chart-key" aria-hidden="true" style="background:var(--viz-2)"></i>amex',
    );
    expect(two).toContain('<span class="chart-legend-value">6</span>');
  });

  it("keys the bands of an area with squares", () => {
    const area = renderPanel(
      panel({
        type: "area",
        series: [{ label: "visa", points: [1, 2, 3], value: 6 }],
      }),
    );
    expect(area).toContain('class="chart-key chart-key-square"');
  });
});

describe("panel width", () => {
  it("spans the row at width 2 and keeps the same frame as a half-row panel", () => {
    const half = renderPanel(panel({ width: 1 }));
    const full = renderPanel(panel({ width: 2 }));
    expect(half).not.toContain("chart-panel-full");
    expect(full).toContain('class="panel chart-panel-full"');
    expect(full).toContain('class="chart chart-fluid"');
    expect(full).toContain('preserveAspectRatio="none"');
  });

  it("treats a missing width as half a row, and a table as the full one", () => {
    expect(panelWidth(panel())).toBe(1);
    expect(panelWidth(panel({ type: "table" }))).toBe(2);
    expect(panelWidth(panel({ type: "table", width: 1 }))).toBe(1);
    expect(panelWidth(panel({ width: 5 }))).toBe(2);
  });

  it("puts the axis labels outside the plot, in the page's type", () => {
    const html = renderPanel(panel());
    expect(html).not.toContain("<text");
    expect(html).toContain('<span class="chart-tick">5</span>');
  });
});

describe("the time axis", () => {
  it("adds the date once the window is longer than half a day", () => {
    const day = renderPanel(
      panel({
        buckets: ["2026-08-17T10:00:00Z", "2026-08-18T09:55:00Z"],
        series: [{ label: "visa", points: [1, 2], value: 3 }],
      }),
    );
    expect(day).toMatch(/Aug\s+\d+/);
  });

  it("leaves the date off a short window, where it is only noise", () => {
    expect(renderPanel(panel())).not.toMatch(/Aug\s+\d+/);
  });

  it("labels the first and the last bucket, or the only one", () => {
    expect(timeAxis([])).toEqual([]);
    expect(timeAxis(["2026-08-17T10:00:00Z"])).toHaveLength(1);
    expect(timeAxis(["2026-08-17T10:00:00Z", "x", "2026-08-17T10:10:00Z"])).toHaveLength(2);
  });
});

describe("rangeLabel", () => {
  it("states the window the panel actually resolved", () => {
    expect(rangeLabel(panel({ from: "2026-08-17T00:00:00Z", to: "2026-08-18T00:00:00Z" }))).toBe(
      "last 24h",
    );
    expect(rangeLabel(panel({ from: "2026-08-10T00:00:00Z", to: "2026-08-17T00:00:00Z" }))).toBe(
      "last 7d",
    );
  });
});
