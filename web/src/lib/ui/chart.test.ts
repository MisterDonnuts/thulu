// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";

import {
  barChart,
  bindCrosshair,
  chartBarRowHTML,
  chartBarsHTML,
  chartClasses,
  chartEmptyHTML,
  chartHTML,
  chartKeyClasses,
  chartLegendClasses,
  chartLegendHTML,
  chartLegendItemsHTML,
  chartPanelClass,
  chartPanelsHTML,
  chartRuns,
  chartTip,
  chartTipHTML,
  chartWindow,
  FLUID_PLOT,
  fluidBandsSVG,
  fluidChartHTML,
  fluidLinesSVG,
  nearestIndex,
  countMax,
  niceMax,
  placeChartTip,
  setChartLegend,
  sparklineSVG,
  stackTotals,
  stackedBarChart,
  timeChart,
  timeTicks,
  type ChartSeries,
  type TimePoint,
} from "./chart";

interface P extends TimePoint {
  a: number;
  b: number;
}

const end = Date.parse("2026-10-03T12:00:00Z");
const step = 15_000;
const at = (ms: number, a: number, b = 0): P => ({ t: new Date(ms).toISOString(), a, b });
const opts = { width: 600, height: 180, rangeMs: 15 * 60_000, end, intervalMs: step, fmtY: String };
const one: Array<ChartSeries<P>> = [{ label: "CPU", color: "var(--viz-1)", value: (p) => p.a }];
const two: Array<ChartSeries<P>> = [
  { label: "In", color: "var(--viz-1)", value: (p) => p.a },
  { label: "Out", color: "var(--viz-3)", value: (p) => p.b },
];

describe("chartClasses", () => {
  it("writes the frame and its height, md by default", () => {
    expect(chartClasses()).toBe("chart");
    expect(chartClasses({ size: "sm", cls: "ic-chart" })).toBe("chart chart-sm ic-chart");
    expect(chartClasses({ size: "auto" })).toBe("chart chart-auto");
  });
  it("writes the fluid frame after the size", () => {
    expect(chartClasses({ fluid: true })).toBe("chart chart-fluid");
    expect(chartClasses({ size: "sm", fluid: true, cls: "x" })).toBe(
      "chart chart-sm chart-fluid x",
    );
  });
  it("throws on a size that does not exist, and on a fluid frame with no height", () => {
    expect(() => chartClasses({ size: "huge" as never })).toThrow(/no size/);
    expect(() => chartClasses({ size: "auto", fluid: true })).toThrow(/needs a height/);
  });
});

describe("chartHTML", () => {
  it("is one named image, in the tab order when interactive", () => {
    expect(chartHTML({ id: "c", label: "CPU: 42%", size: "sm", interactive: true })).toBe(
      '<div id="c" class="chart chart-sm" role="img" aria-label="CPU: 42%" tabindex="0"></div>',
    );
  });
  it("holds the body as given and escapes the label", () => {
    const html = chartHTML({ label: '<b>"x"', bodyHTML: "<svg></svg>", attrs: { hidden: true } });
    expect(html).toBe(
      '<div class="chart" role="img" aria-label="&lt;b&gt;&quot;x&quot;" hidden><svg></svg></div>',
    );
  });
});

describe("chartEmptyHTML", () => {
  it("is the small dashed empty state with its glyph", () => {
    const html = chartEmptyHTML("Warming <up>", { icon: "i-clock" });
    expect(html).toContain('class="empty-state empty-state-sm empty-state-dashed"');
    expect(html).toContain("Warming &lt;up&gt;");
    expect(html).toContain("#i-clock");
  });
});

describe("chartLegendHTML", () => {
  it("writes a key and a name per series, line keys by default", () => {
    expect(
      chartLegendHTML(
        [
          { label: "In", color: "var(--viz-1)" },
          { label: "Out", color: "var(--viz-3)" },
        ],
        { placement: "head", id: "legend-net" },
      ),
    ).toBe(
      '<div id="legend-net" class="chart-legend chart-legend-head">' +
        '<span class="chart-legend-item"><i class="chart-key" aria-hidden="true" style="background:var(--viz-1)"></i>In</span>' +
        '<span class="chart-legend-item"><i class="chart-key" aria-hidden="true" style="background:var(--viz-3)"></i>Out</span>' +
        "</div>",
    );
  });
  it("draws square keys and a figure after the name, escaped", () => {
    const html = chartLegendHTML([
      { label: "a<b", color: "var(--ok)", swatch: "square", value: "12 <ms>" },
    ]);
    expect(html).toContain('class="chart-legend chart-legend-body"');
    expect(html).toContain('class="chart-key chart-key-square"');
    expect(html).toContain('a&lt;b<span class="chart-legend-value">12 &lt;ms&gt;</span>');
  });
  it("leaves an empty box for a script to fill", () => {
    expect(chartLegendHTML([], { id: "l" })).toBe(
      '<div id="l" class="chart-legend chart-legend-body"></div>',
    );
  });
  it("throws on a placement or a swatch that does not exist", () => {
    expect(() => chartLegendClasses({ placement: "side" as never })).toThrow(/placement/);
    expect(() => chartKeyClasses("dot" as never)).toThrow(/swatch/);
  });
});

describe("chartTipHTML", () => {
  it("writes the time, then a row per series with key, figure and name", () => {
    expect(
      chartTipHTML({
        time: "18:48:14",
        rows: [{ value: "42%", label: "CPU", color: "var(--viz-1)" }],
      }),
    ).toBe(
      '<div class="chart-tip-time">18:48:14</div>' +
        '<div class="chart-tip-row"><i class="chart-key" aria-hidden="true" style="background:var(--viz-1)"></i>' +
        '<strong class="chart-tip-value">42%</strong><span class="chart-tip-label">CPU</span></div>',
    );
  });
  it("drops the key and the name a lone series does not need, and escapes", () => {
    expect(chartTipHTML({ time: "3 <oct>", rows: [{ value: "$3.85" }] })).toBe(
      '<div class="chart-tip-time">3 &lt;oct&gt;</div>' +
        '<div class="chart-tip-row"><strong class="chart-tip-value">$3.85</strong></div>',
    );
  });
});

describe("chartTip and placeChartTip", () => {
  it("makes the tooltip once, hidden, and flips it on the right half", () => {
    const host = document.createElement("div");
    const tip = chartTip(host);
    expect(chartTip(host)).toBe(tip);
    expect(tip.className).toBe("chart-tip");
    expect(tip.hidden).toBe(true);
    placeChartTip(tip, 100, 600);
    expect(tip.style.left).toBe("100px");
    expect(tip.classList.contains("chart-tip-left")).toBe(false);
    placeChartTip(tip, 500, 600);
    expect(tip.classList.contains("chart-tip-left")).toBe(true);
  });
});

describe("niceMax", () => {
  it("rounds up to 1, 2 or 5 of a power of ten", () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(7)).toBe(10);
    expect(niceMax(23)).toBe(50);
    expect(niceMax(120)).toBe(200);
  });
});

describe("countMax", () => {
  it("rounds whole counts up to an even top, so the midpoint is whole too", () => {
    expect(countMax(1)).toBe(2);
    expect(countMax(3)).toBe(4);
    expect(countMax(4)).toBe(4);
    expect(countMax(7)).toBe(10);
    expect(countMax(11)).toBe(20);
    for (let n = 1; n <= 200; n++) {
      expect(countMax(n)).toBeGreaterThanOrEqual(n);
      expect(countMax(n) % 2).toBe(0);
    }
  });
});

describe("timeChart", () => {
  it("drops points older than the window", () => {
    const pts = [
      at(end - 20 * 60_000, 10),
      at(end - 2 * step, 20),
      at(end - step, 30),
      at(end, 40),
    ];
    const m = timeChart(pts, one, opts);
    expect(m.xs).toHaveLength(3);
    expect(m.xs.at(-1)).toBeCloseTo(600 - 12);
  });

  it("washes the area under a lone series only, unless told otherwise", () => {
    const pts = [at(end - 2 * step, 20), at(end - step, 30), at(end, 40)];
    expect(timeChart(pts, one, opts).svg).toContain('class="chart-wash"');
    expect(timeChart(pts, one, { ...opts, wash: false }).svg).not.toContain("chart-wash");
    const svg = timeChart(pts, two, opts).svg;
    expect(svg).not.toContain("chart-wash");
    expect(svg.match(/class="chart-line"/g)).toHaveLength(2);
    expect(svg.match(/class="chart-dot"/g)).toHaveLength(2);
    expect(svg).toContain('class="chart-cross"');
    expect(svg.match(/class="chart-grid"/g)).toHaveLength(3);
  });

  it("breaks the line across a hole longer than three samples", () => {
    const pts = [
      at(end - 10 * 60_000, 10),
      at(end - 10 * 60_000 + step, 10),
      at(end - step, 30),
      at(end, 40),
    ];
    expect(timeChart(pts, one, opts).svg.match(/class="chart-line"/g)).toHaveLength(2);
  });

  it("labels the time axis with fmtX when given", () => {
    const pts = [at(end - step, 1), at(end, 2)];
    const svg = timeChart(pts, one, { ...opts, fmtX: () => "3 oct" }).svg;
    expect(svg.match(/>3 oct</g)).toHaveLength(timeTicks(600 - 64 - 12) + 1);
  });

  it("caps values at a fixed maximum and escapes the axis labels", () => {
    const pts = [at(end - step, 50), at(end, 250)];
    expect(timeChart(pts, one, { ...opts, yMax: 100 }).ys[0][1]).toBeCloseTo(10);
    const svg = timeChart(pts, one, { ...opts, fmtY: () => "<b>" }).svg;
    expect(svg).not.toContain("<b>");
    expect(svg).toContain("&lt;b&gt;");
  });
});

describe("timeTicks, chartWindow and nearestIndex", () => {
  it("cuts a narrow axis into fewer intervals", () => {
    expect([timeTicks(260), timeTicks(400), timeTicks(900)]).toEqual([2, 3, 4]);
  });
  it("shrinks the window to a young history, not under the floor", () => {
    const hour = 60 * 60_000;
    expect(chartWindow([at(end - 2 * hour, 1), at(end, 1)], hour)).toBe(hour);
    expect(chartWindow([at(end - 12 * 60_000, 1), at(end, 1)], hour)).toBe(12 * 60_000);
    expect(chartWindow([at(end - 30_000, 1), at(end, 1)], hour)).toBe(2 * 60_000);
  });
  it("picks the closest x, or -1 for no points", () => {
    expect(nearestIndex([10, 20, 30], 24)).toBe(1);
    expect(nearestIndex([], 5)).toBe(-1);
  });
});

describe("bindCrosshair", () => {
  afterEach(() => document.body.replaceChildren());

  function drawn() {
    const pts = [at(end - 2 * step, 20, 1), at(end - step, 30, 2), at(end, 40, 3)];
    const host = document.createElement("div");
    host.innerHTML = chartHTML({ interactive: true });
    const frame = host.firstElementChild as HTMLElement;
    document.body.appendChild(host);
    const model = timeChart(pts, two, opts);
    frame.innerHTML = model.svg;
    const off = bindCrosshair(frame, model, pts, two, (v) => `${v}%`, {
      fmtTime: () => "T",
    });
    return { frame, off };
  }
  function find<E extends Element = HTMLElement>(root: ParentNode, sel: string): E {
    const el = root.querySelector<E>(sel);
    if (!el) throw new Error(`no ${sel}`);
    return el;
  }
  const key = (el: HTMLElement, k: string) =>
    el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));

  it("walks the samples with the keys and lists every series in the tooltip", () => {
    const { frame } = drawn();
    const tip = find(frame, ".chart-tip");
    expect(tip.hidden).toBe(true);
    key(frame, "ArrowRight");
    expect(tip.hidden).toBe(false);
    expect(find(tip, ".chart-tip-time").textContent).toBe("T");
    expect([...tip.querySelectorAll(".chart-tip-value")].map((e) => e.textContent)).toEqual([
      "40%",
      "3%",
    ]);
    key(frame, "Home");
    expect(find(tip, ".chart-tip-value").textContent).toBe("20%");
    expect(find(frame, ".chart-cross").getAttribute("visibility")).toBe("visible");
    key(frame, "Escape");
    expect(tip.hidden).toBe(true);
    expect(find(frame, ".chart-cross").getAttribute("visibility")).toBe("hidden");
  });

  it("stops answering once unbound", () => {
    const { frame, off } = drawn();
    off();
    key(frame, "End");
    expect(find(frame, ".chart-tip").hidden).toBe(true);
  });
});

describe("stackedBarChart", () => {
  const bars = [
    {
      label: "6 Oct",
      title: "busy <day>",
      parts: [
        { value: 2, color: "var(--ok)" },
        { value: 1, color: "var(--accent)" },
        { value: 0, color: "var(--warn)" },
      ],
    },
    { label: "7 Oct", title: "quiet", parts: [{ value: 0, color: "var(--ok)" }] },
  ];

  it("stacks the parts bottom up, skips zeros and titles every bar", () => {
    const svg = stackedBarChart(bars, { width: 400, height: 200 });
    expect(svg.match(/<g class="chart-bar">/g)).toHaveLength(2);
    expect(svg.match(/class="chart-hit"/g)).toHaveLength(2);
    expect(svg.match(/fill="var\(--/g)).toHaveLength(2);
    expect(svg).toContain("<title>busy &lt;day&gt;</title>");
    const ys = [...svg.matchAll(/<rect x="[\d.]+" y="([\d.]+)"[^>]*fill="var/g)].map((m) =>
      Number(m[1]),
    );
    expect(ys[0]).toBeGreaterThan(ys[1]);
  });

  it("labels the first, middle and last bar, and draws nothing from nothing", () => {
    const svg = stackedBarChart(bars, { width: 400, height: 200, fmtY: (v) => `${v}x` });
    expect(svg).toContain(">6 Oct<");
    expect(svg).toContain(">7 Oct<");
    expect(svg).toContain(">5x<");
    const empty = stackedBarChart([], { width: 300, height: 200 });
    expect(empty).toContain("<svg");
    expect(empty).not.toContain("NaN");
  });
});

describe("barChart", () => {
  const days = [
    {
      label: "6 Oct",
      title: "6 Oct: 4 created, 2 completed",
      parts: [
        { value: 4, color: "var(--viz-8)" },
        { value: 2, color: "var(--viz-1)" },
      ],
    },
    {
      label: "7 Oct",
      title: "7 Oct: 0 created, 3 completed",
      parts: [
        { value: 0, color: "var(--viz-8)" },
        { value: 3, color: "var(--viz-1)" },
      ],
    },
  ];
  const rects = (svg: string) =>
    [...svg.matchAll(/<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)"/g)].map(
      (m) => ({ x: Number(m[1]), y: Number(m[2]), w: Number(m[3]), h: Number(m[4]) }),
    );

  it("stands grouped parts side by side on the same baseline", () => {
    const svg = barChart(days, { width: 400, height: 200, layout: "grouped" });
    const [created, completed] = rects(svg);
    expect(completed.x).toBeGreaterThan(created.x + created.w - 0.01);
    expect(created.y + created.h).toBeCloseTo(completed.y + completed.h, 1);
    expect(svg).toContain(">5<");
    expect(svg).not.toContain(">10<");
    expect(svg).toContain("<title>6 Oct: 4 created, 2 completed</title>");
  });

  it("stacks by default, the axis reaching the tallest total", () => {
    const svg = barChart(days, { width: 400, height: 200 });
    const [a, b] = rects(svg);
    expect(a.x).toBe(b.x);
    expect(b.y + b.h).toBeCloseTo(a.y, 1);
    expect(svg).toContain(">10<");
    expect(stackedBarChart(days, { width: 400, height: 200 })).toBe(svg);
  });

  it("marks a count axis in whole numbers only", () => {
    const ticks = (svg: string) =>
      [...svg.matchAll(/<text class="chart-tick" x="30.0"[^>]*>([^<]*)</g)].map((m) => m[1]);
    const one = [{ label: "6 Oct", title: "6 Oct", parts: [{ value: 3, color: "var(--viz-1)" }] }];
    expect(ticks(barChart(one, { width: 400, height: 200 }))).toContain("2.5");
    expect(ticks(barChart(one, { width: 400, height: 200, counts: true }))).toEqual([
      "0",
      "2",
      "4",
    ]);
    const lone = [{ label: "6 Oct", title: "6 Oct", parts: [{ value: 1, color: "var(--viz-1)" }] }];
    expect(ticks(barChart(lone, { width: 400, height: 200, counts: true }))).toEqual([
      "0",
      "1",
      "2",
    ]);
  });

  it("skips a zero part and throws on a layout that does not exist", () => {
    const svg = barChart(days, { width: 400, height: 200, layout: "grouped" });
    expect(rects(svg)).toHaveLength(3);
    expect(() => barChart(days, { width: 400, height: 200, layout: "pie" as never })).toThrow(
      /no bar layout/,
    );
  });
});

describe("chartRuns", () => {
  it("breaks a series at a gap instead of bridging it", () => {
    expect(chartRuns([1, 2, null, 4])).toEqual([
      [
        { i: 0, v: 1 },
        { i: 1, v: 2 },
      ],
      [{ i: 3, v: 4 }],
    ]);
    expect(chartRuns([1, undefined, Number.NaN, 2])).toHaveLength(2);
  });

  it("treats a series with no observations as no runs", () => {
    expect(chartRuns([null, null])).toEqual([]);
  });
});

describe("fluid charts", () => {
  it("draws a line per run and a dot for a lone sample, across the full box", () => {
    const svg = fluidLinesSVG([{ color: "var(--viz-2)", points: [0, 5, null, 10] }], 4, 10);
    const lines = [...svg.matchAll(/<polyline class="([^"]+)" stroke="([^"]+)" points="([^"]+)"/g)];
    expect(lines).toHaveLength(2);
    expect(lines[0][1]).toBe("chart-line");
    expect(lines[0][3]).toBe(`0,${FLUID_PLOT.h} 333.3,50`);
    expect(lines[1][1]).toBe("chart-line chart-point");
    expect(lines[1][3]).toBe(`${FLUID_PLOT.w},0 ${FLUID_PLOT.w},0`);
  });

  it("centres a lone slot and caps a value over the axis top", () => {
    expect(fluidLinesSVG([{ color: "c", points: [20] }], 1, 10)).toContain(
      `points="${FLUID_PLOT.w / 2},0 ${FLUID_PLOT.w / 2},0"`,
    );
  });

  it("stacks bands on the ones below, a gap adding nothing", () => {
    const series = [
      { color: "var(--viz-1)", points: [1, null, 1] },
      { color: "var(--viz-2)", points: [1, 1, 1] },
    ];
    expect(stackTotals(series, 3)).toEqual([2, 1, 2]);
    const svg = fluidBandsSVG(series, 3, 2);
    const bands = [...svg.matchAll(/<polygon class="chart-band" fill="([^"]+)" points="([^"]+)"/g)];
    expect(bands.map((b) => b[1])).toEqual(["var(--viz-1)", "var(--viz-2)"]);
    expect(bands[1][2]).toContain(`${FLUID_PLOT.w},50`);
    expect(svg).not.toContain("NaN");
  });

  it("frames the plot with its axes in HTML and a gridline per y label", () => {
    const html = fluidChartHTML({
      label: "Errors <24h>",
      yTicks: ["0", "5", "10"],
      xTicks: ["10:00", "<10:15>"],
      plotSVG: "<polyline/>",
      cls: "x",
      attrs: { "data-panel": "p1" },
    });
    expect(html).toMatch(
      /^<div class="chart chart-fluid x" role="img" aria-label="Errors &lt;24h&gt;" data-panel="p1">/,
    );
    expect(html.match(/class="chart-grid"/g)).toHaveLength(3);
    expect(html).toContain(
      '<div class="chart-axis chart-axis-y" aria-hidden="true"><span class="chart-tick">10</span><span class="chart-tick">5</span><span class="chart-tick">0</span></div>',
    );
    expect(html).toContain("&lt;10:15&gt;");
    expect(html).toContain('preserveAspectRatio="none"');
    expect(html).toContain("<polyline/>");
  });

  it("leaves the x axis out when there is nothing to label", () => {
    expect(fluidChartHTML({ yTicks: ["0", "1"], plotSVG: "" })).not.toContain("chart-axis-x");
  });
});

describe("chartBarsHTML", () => {
  it("writes a row per name with its meter, its figure and the quieter one", () => {
    const html = chartBarsHTML([
      { label: "In <review>", share: 100, value: "2h", sub: "p90 5h" },
      { label: "Done", share: 25, value: "30m", tone: "viz-2" },
    ]);
    expect(html.match(/class="chart-bars-row"/g)).toHaveLength(2);
    expect(html).toContain('title="In &lt;review&gt;">In &lt;review&gt;</span>');
    expect(html).toContain('role="meter"');
    expect(html).toContain('aria-valuetext="2h"');
    expect(html).toContain('<span class="chart-bars-sub">p90 5h</span>');
    expect(html).toContain('data-tone="viz-2"');
    expect(html).toContain("width:25%");
  });
  it("takes an id and a placement class", () => {
    expect(chartBarsHTML([], { id: "b", cls: "x" })).toBe(
      '<div id="b" class="chart-bars x"></div>',
    );
  });
  it("is the rows chartBarRowHTML writes, one after another", () => {
    const rows = [
      { label: "a", share: 50, value: "1" },
      { label: "b", share: 100, value: "2" },
    ];
    expect(chartBarsHTML(rows)).toBe(
      `<div class="chart-bars">${rows.map(chartBarRowHTML).join("")}</div>`,
    );
  });
  it("names a row in its tooltip with the title when there is one", () => {
    const row = chartBarRowHTML({
      label: "jobs",
      share: 10,
      value: "1 MB",
      title: "jobs: <3> rows",
    });
    expect(row).toContain('title="jobs: &lt;3&gt; rows">jobs</span>');
    expect(row).toContain('aria-label="jobs"');
  });
  it("reads the meter out with its own text when the figure has no unit", () => {
    const row = chartBarRowHTML({
      label: "end_turn",
      share: 64.5,
      value: "40",
      sub: "64.5%",
      valueText: "40 sessions (64.5%)",
    });
    expect(row).toContain('aria-valuetext="40 sessions (64.5%)"');
    expect(row).toContain(
      '<span class="chart-bars-value">40<span class="chart-bars-sub">64.5%</span>',
    );
    expect(chartBarRowHTML({ label: "a", share: 1, value: "1" })).toContain('aria-valuetext="1"');
  });
  it("sets an identifier's name in the monospace face when asked", () => {
    const row = chartBarRowHTML({ label: "end_turn", share: 40, value: "4", mono: true });
    expect(row).toContain('<span class="chart-bars-label chart-bars-label-mono" title="end_turn">');
    expect(chartBarRowHTML({ label: "a", share: 1, value: "1" })).toContain(
      '<span class="chart-bars-label" title="a">',
    );
  });
});

describe("chartPanelsHTML", () => {
  it("holds the panels, a full-row one marked to span the row", () => {
    expect(chartPanelsHTML(["<a></a>", "<b></b>"])).toBe(
      '<div class="chart-panels"><a></a><b></b></div>',
    );
    expect(chartPanelClass(true)).toBe("chart-panel-full");
    expect(chartPanelClass(false)).toBeUndefined();
  });
});

describe("chartLegendItemsHTML", () => {
  it("writes a key and a name per series, both escaped", () => {
    expect(chartLegendItemsHTML([{ label: "a<b", color: 'var(--viz-1)"', swatch: "square" }])).toBe(
      '<span class="chart-legend-item"><i class="chart-key chart-key-square" aria-hidden="true" style="background:var(--viz-1)&quot;"></i>a&lt;b</span>',
    );
  });
});

describe("sparklineSVG", () => {
  it("breaks the trace at a gap and scales to the observed values", () => {
    const svg = sparklineSVG([0, 10, null, 5, 10]);
    expect(svg.match(/<polyline/g)).toHaveLength(2);
    expect(svg).toContain('points="0,18 17,0"');
    expect(sparklineSVG([1, null, 2])).toBe("");
    expect(sparklineSVG([1, null])).toBe("");
  });
  it("needs two points, and spans min to max", () => {
    expect(sparklineSVG([5])).toBe("");
    const svg = sparklineSVG([0, 10]);
    expect(svg).toContain('class="chart-spark"');
    expect(svg).toContain('class="chart-spark-line" points="0,18 68,0"');
    expect(sparklineSVG([0, 10], { width: 10, height: 2 })).toContain('points="0,2 10,0"');
  });
  it("draws a flat series without dividing by zero", () => {
    expect(sparklineSVG([3, 3, 3])).not.toContain("NaN");
  });
});

describe("setChartLegend", () => {
  it("fills a legend already on the page with the same items chartLegendHTML writes", () => {
    const items = [
      { label: "In", color: "var(--viz-1)" },
      { label: "a<b", color: "var(--ok)", swatch: "square" as const },
    ];
    const host = document.createElement("div");
    host.innerHTML = chartLegendHTML([], { id: "legend-net", placement: "head" });
    const legend = host.firstElementChild as HTMLElement;
    setChartLegend(legend, items);
    expect(host.innerHTML).toBe(chartLegendHTML(items, { id: "legend-net", placement: "head" }));
    expect(legend.innerHTML).toBe(chartLegendItemsHTML(items));
  });
  it("leaves the nodes alone when the keys did not change, and empties it for none", () => {
    const legend = document.createElement("div");
    setChartLegend(legend, [{ label: "In", color: "var(--viz-1)" }]);
    const first = legend.firstChild;
    setChartLegend(legend, [{ label: "In", color: "var(--viz-1)" }]);
    expect(legend.firstChild).toBe(first);
    setChartLegend(legend, []);
    expect(legend.innerHTML).toBe("");
  });
});
