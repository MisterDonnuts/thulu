import { describe, expect, it } from "vitest";
import {
  applyPanelEdit,
  blankPanel,
  chartFits,
  panelRowHTML,
  type ChartCatalog,
  type PanelSpec,
} from "./dash-panels";
import type { CustomMetric } from "./env-cards";

function metric(over: Partial<CustomMetric> = {}): CustomMetric {
  return {
    id: "cm1",
    service: "api",
    name: "orders_total",
    kind: "counter",
    labels: ["region", "plan"],
    label_limit: 10,
    status: "approved",
    first_seen: "2026-07-31T10:00:00Z",
    last_seen: "2026-07-31T11:00:00Z",
    ...over,
  };
}

const catalog: ChartCatalog = {
  charts: [
    {
      type: "line",
      title: "Line",
      description: "A time series",
      min_series: 1,
      max_series: 4,
      group_by: "optional",
      time_axis: true,
    },
    {
      type: "stat",
      title: "Stat",
      description: "One number",
      min_series: 1,
      max_series: 1,
      group_by: "forbidden",
      time_axis: false,
    },
    {
      type: "bar",
      title: "Bar",
      description: "Split by a label",
      min_series: 1,
      max_series: 1,
      group_by: "required",
      time_axis: false,
    },
    {
      type: "area",
      title: "Stacked area",
      description: "Counters split by a label",
      min_series: 1,
      max_series: 1,
      group_by: "required",
      kinds: ["counter"],
      time_axis: true,
    },
  ],
  aggs_by_kind: { counter: ["sum", "rate"], gauge: ["last", "avg"] },
};

function panel(over: Partial<PanelSpec> = {}): PanelSpec {
  return {
    type: "line",
    title: "Orders",
    range_hours: 24,
    width: 1,
    series: [{ metric: "orders_total", service: "api", agg: "sum" }],
    ...over,
  };
}

describe("blankPanel", () => {
  it("is null when nothing has been approved", () => {
    expect(blankPanel([])).toBeNull();
  });
  it("sums a counter and takes the last of a gauge", () => {
    expect(blankPanel([metric()])?.series[0].agg).toBe("sum");
    expect(blankPanel([metric({ kind: "gauge" })])?.series[0].agg).toBe("last");
  });
  it("titles itself with the display name when there is one", () => {
    expect(blankPanel([metric({ display_name: "Orders placed" })])?.title).toBe("Orders placed");
    expect(blankPanel([metric()])?.title).toBe("orders_total");
  });
});

describe("panelRowHTML", () => {
  it("marks the current type, metric and aggregation as selected", () => {
    const html = panelRowHTML(panel(), 0, [metric()], catalog);
    expect(html).toContain('<option value="line" selected');
    expect(html).toContain('<option value="0" selected>');
    expect(html).toContain('<option value="sum" selected>');
  });
  it("offers only the aggregations legal for the metric's kind", () => {
    const html = panelRowHTML(
      panel({ series: [{ metric: "cpu", service: "api", agg: "last" }] }),
      0,
      [metric({ name: "cpu", kind: "gauge" })],
      catalog,
    );
    expect(html).toContain(">last<");
    expect(html).not.toContain(">rate<");
  });
  it("disables the label split for a chart that forbids it", () => {
    const html = panelRowHTML(panel({ type: "stat" }), 0, [metric()], catalog);
    expect(html).toContain('data-dp-field="group_by" disabled');
  });
  it("refuses to flatten a multi-series panel into one picker", () => {
    const p = panel({
      series: [
        { metric: "a", service: "api", agg: "sum" },
        { metric: "b", service: "api", agg: "sum" },
      ],
    });
    const html = panelRowHTML(p, 0, [metric()], catalog);
    expect(html).toContain("2 series — edit them through the API");
    expect(html).not.toContain('data-dp-field="metric"');
  });
  it("carries its index on every control", () => {
    expect(panelRowHTML(panel(), 3, [metric()], catalog)).toContain('data-dp-del="3"');
  });
  it("draws the remove control as a named icon-only danger button", () => {
    const html = panelRowHTML(panel(), 3, [metric()], catalog);
    const btn = html.match(/<button[^>]*data-dp-del="3"[^>]*>/)?.[0] ?? "";
    expect(btn).toContain('class="icon-btn icon-btn-sm icon-btn-danger"');
    expect(btn).toContain('type="button"');
    expect(btn).toMatch(/aria-label="[^"]+"/);
    expect(html).toContain('<svg class="ico" aria-hidden="true"><use href="#i-trash"/></svg>');
  });
  it("is a small panel of two rows of small fields, each labelled for its control", () => {
    const html = panelRowHTML(panel(), 2, [metric()], catalog);
    expect(html).toMatch(/^<div class="panel panel-sm">/);
    expect(html.match(/<div class="form-row">/g)).toHaveLength(2);
    for (const f of ["title", "type", "metric", "agg", "group_by", "range", "width"]) {
      expect(html).toContain(`<label class="field-label" for="dp-2-${f}">`);
      expect(html).toContain(`id="dp-2-${f}"`);
    }
    expect(html.match(/class="field field-sm field-grow"/g)).toHaveLength(2);
  });
  it("escapes a title the user typed", () => {
    expect(panelRowHTML(panel({ title: '"><script>' }), 0, [metric()], catalog)).not.toContain(
      "<script>",
    );
  });
});

describe("chartFits", () => {
  const chart = (type: string) => {
    const c = catalog.charts.find((x) => x.type === type);
    if (!c) throw new Error(`no ${type} chart in the test catalog`);
    return c;
  };
  it("keeps a stacked area away from a gauge", () => {
    expect(chartFits(chart("area"), metric())).toBe(true);
    expect(chartFits(chart("area"), metric({ kind: "gauge" }))).toBe(false);
  });
  it("keeps a chart that splits by label away from a metric with none", () => {
    expect(chartFits(chart("bar"), metric({ labels: [] }))).toBe(false);
    expect(chartFits(chart("line"), metric({ labels: [] }))).toBe(true);
  });
  it("offers everything when there is no single metric to judge", () => {
    expect(chartFits(chart("area"), undefined)).toBe(true);
  });
});

describe("panelRowHTML fields", () => {
  it("names every control with a visible label tied to it", () => {
    const html = panelRowHTML(panel(), 0, [metric()], catalog);
    const fields: Array<[string, string]> = [
      ["Title", "title"],
      ["Chart", "type"],
      ["Metric", "metric"],
      ["Aggregation", "agg"],
      ["Split by", "group_by"],
      ["Hours", "range"],
      ["Width", "width"],
    ];
    for (const [label, key] of fields) {
      expect(html).toContain(`<label class="field-label" for="dp-0-${key}">${label}</label>`);
      expect(html).toContain(`id="dp-0-${key}"`);
    }
  });
  it("lists only the charts the metric fits, plus the current one", () => {
    const gauge = metric({ kind: "gauge", labels: [] });
    const series = [{ metric: "orders_total", service: "api", agg: "last" }];
    const html = panelRowHTML(panel({ series }), 0, [gauge], catalog);
    expect(html).toContain('value="line"');
    expect(html).toContain('value="stat"');
    expect(html).not.toContain('value="bar"');
    expect(html).not.toContain('value="area"');
    expect(panelRowHTML(panel({ type: "bar", series }), 0, [gauge], catalog)).toContain(
      'value="bar"',
    );
  });
});

describe("applyPanelEdit", () => {
  it("does not ask for a redraw on a plain field", () => {
    const p = panel();
    expect(applyPanelEdit(p, "title", "New name", [metric()], catalog)).toBe(false);
    expect(p.title).toBe("New name");
  });
  it("drops group_by when the new chart forbids it", () => {
    const p = panel({ group_by: "region" });
    expect(applyPanelEdit(p, "type", "stat", [metric()], catalog)).toBe(true);
    expect(p.group_by).toBe("");
  });
  it("fills group_by from the metric when the new chart requires it", () => {
    const p = panel();
    applyPanelEdit(p, "type", "bar", [metric()], catalog);
    expect(p.group_by).toBe("region");
  });
  it("leaves an existing group_by alone on a chart that requires one", () => {
    const p = panel({ group_by: "plan" });
    applyPanelEdit(p, "type", "bar", [metric()], catalog);
    expect(p.group_by).toBe("plan");
  });
  it("re-picks an aggregation the new metric's kind allows", () => {
    const metrics = [metric(), metric({ id: "cm2", name: "cpu", kind: "gauge", labels: [] })];
    const p = panel();
    expect(applyPanelEdit(p, "metric", "1", metrics, catalog)).toBe(true);
    expect(p.series[0]).toEqual({ metric: "cpu", service: "api", agg: "last" });
  });
  it("drops a group_by the new metric does not carry", () => {
    const metrics = [metric(), metric({ id: "cm2", name: "cpu", kind: "gauge", labels: [] })];
    const p = panel({ group_by: "region" });
    applyPanelEdit(p, "metric", "1", metrics, catalog);
    expect(p.group_by).toBe("");
  });
  it("leaves a chart the new metric cannot fill for the first that fits", () => {
    const metrics = [metric(), metric({ id: "cm2", name: "cpu", kind: "gauge", labels: [] })];
    const p = panel({ type: "area", group_by: "region" });
    applyPanelEdit(p, "metric", "1", metrics, catalog);
    expect(p.type).toBe("line");
    expect(p.group_by).toBe("");
  });
  it("ignores a metric index that does not exist", () => {
    const p = panel();
    expect(applyPanelEdit(p, "metric", "9", [metric()], catalog)).toBe(false);
    expect(p.series[0].metric).toBe("orders_total");
  });
  it("falls back to a day when the range is cleared", () => {
    const p = panel();
    applyPanelEdit(p, "range_hours", "", [metric()], catalog);
    expect(p.range_hours).toBe(24);
    applyPanelEdit(p, "range_hours", "72", [metric()], catalog);
    expect(p.range_hours).toBe(72);
  });
  it("reads the width back as a number", () => {
    const p = panel();
    applyPanelEdit(p, "width", "2", [metric()], catalog);
    expect(p.width).toBe(2);
  });
});
