import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import {
  chartHTML,
  chartLegendHTML,
  type ChartLegendItem,
  type ChartLegendOptions,
  type ChartOptions,
} from "../lib/ui/chart";
import Chart from "./Chart.astro";
import ChartLegend from "./ChartLegend.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Chart.astro and chartHTML()", () => {
  const cases: Array<{ name: string; o: ChartOptions }> = [
    { name: "a bare frame for a script to fill", o: { id: "chart-cpu" } },
    {
      name: "small, interactive, placed by the page",
      o: { id: "chart-net", size: "sm", interactive: true, cls: "ic-chart" },
    },
    {
      name: "a named image that scales with its viewBox, with hooks",
      o: {
        size: "auto",
        label: 'Cost "last 30 days"',
        attrs: { "data-panel": "p1", hidden: true },
      },
    },
    { name: "the tallest frame", o: { size: "xl", label: "Cost" } },
    { name: "a fluid frame for a plot that stretches", o: { fluid: true, label: "Errors" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...rest } = c.o;
      const astro = await container.renderToString(Chart, {
        props: { ...rest, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(chartHTML(c.o)));
    });
  }

  it("holds what its slot holds, as chartHTML holds bodyHTML", async () => {
    const svg = '<svg viewBox="0 0 10 10"><line class="chart-grid"></line></svg>';
    const astro = await container.renderToString(Chart, {
      props: { size: "lg" },
      slots: { default: svg },
    });
    expect(normalize(astro)).toBe(normalize(chartHTML({ size: "lg", bodyHTML: svg })));
  });

  it("refuses a size that does not exist, like chartHTML", async () => {
    await expect(container.renderToString(Chart, { props: { size: "huge" } })).rejects.toThrow(
      /no size/,
    );
  });
});

describe("ChartLegend.astro and chartLegendHTML()", () => {
  const NET: ChartLegendItem[] = [
    { label: "In", color: "var(--viz-1)" },
    { label: "Out", color: "var(--viz-3)" },
  ];
  const cases: Array<{ name: string; items: ChartLegendItem[]; o?: ChartLegendOptions }> = [
    { name: "line keys in a head", items: NET, o: { placement: "head", id: "legend-net" } },
    {
      name: "square keys under the plot, with figures and markup in a name",
      items: [
        { label: "Already there", color: "var(--ok)", swatch: "square", value: "12" },
        { label: "Failed <b>", color: "var(--danger)", swatch: "square", value: "0" },
      ],
      o: { cls: "cache-legend", attrs: { "data-k": "1" } },
    },
    { name: "an empty box for a script", items: [], o: { id: "cache-legend", placement: "head" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...rest } = c.o ?? {};
      const astro = await container.renderToString(ChartLegend, {
        props: { items: c.items, ...rest, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(chartLegendHTML(c.items, c.o)));
    });
  }

  it("renders an empty legend with nothing inside, so :empty hides it", async () => {
    const astro = await container.renderToString(ChartLegend, { props: { items: [] } });
    expect(astro).toMatch(/<div class="chart-legend chart-legend-body"[^>]*><\/div>/);
  });
});
