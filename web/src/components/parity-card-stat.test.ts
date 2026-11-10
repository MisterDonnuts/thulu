import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { badgeHTML } from "../lib/ui/badge";
import { buttonHTML } from "../lib/ui/button";
import { cardHTML, type CardOptions } from "../lib/ui/card";
import { esc } from "../lib/ui/dom";
import {
  statGroupHTML,
  statTileHTML,
  type StatGroupOptions,
  type StatOptions,
} from "../lib/ui/stat";
import Card from "./Card.astro";
import { normalize } from "./parity-normalize";
import StatGroup from "./StatGroup.astro";
import StatTile from "./StatTile.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Card.astro and cardHTML()", () => {
  const menu = buttonHTML({ label: "Actions", icon: "more", iconOnly: true, cls: "x-menu" });
  const cases: Array<{ name: string; ts: CardOptions }> = [
    { name: "a title alone", ts: { title: "Sandbox" } },
    {
      name: "a link with badges, a body and a foot",
      ts: {
        title: "Product <main>",
        href: "/settings/boards/?id=a&b=1",
        headHTML: badgeHTML("Default", "accent") + menu,
        bodyHTML: "<p>Three columns.</p>",
        footHTML: buttonHTML({ label: "Run", size: "sm" }),
        attrs: { "data-board": "product" },
      },
    },
    {
      name: "a poster, h2, in a list",
      ts: {
        title: "EV-2",
        heading: "h2",
        tag: "li",
        mediaHTML: '<img src="/p.png" alt="">',
        bodyHTML: "<ul><li>Staging</li></ul>",
      },
    },
    {
      name: "the variants with an id and extras",
      ts: {
        title: "Sandbox",
        reveal: "hover",
        dashed: true,
        off: true,
        id: "env-sb",
        cls: "ev-env",
        tag: "div",
        footHTML: buttonHTML({ label: "Edit", size: "sm" }),
      },
    },
    { name: "a head with no title", ts: { headHTML: badgeHTML("Off", "muted") } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { headHTML, mediaHTML, bodyHTML, footHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (headHTML) slots.head = headHTML;
      if (mediaHTML) slots.media = mediaHTML;
      if (bodyHTML) slots.default = bodyHTML;
      if (footHTML) slots.foot = footHTML;
      const astro = await container.renderToString(Card, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(cardHTML(c.ts)));
    });
  }

  it("refuses a heading it does not draw, like cardHTML", async () => {
    await expect(
      container.renderToString(Card, { props: { title: "x", heading: "h5" } }),
    ).rejects.toThrow(/no heading "h5"/);
  });
});

describe("StatTile.astro and statTileHTML()", () => {
  const cases: Array<{ name: string; ts: StatOptions }> = [
    { name: "label, value and detail", ts: { label: "Open <tasks>", value: 5, sub: "2 & 3" } },
    { name: "a zero with no detail", ts: { label: "Errors", value: 0 } },
    {
      name: "a link with a glyph and a tone",
      ts: { label: "Cost", value: "$12", href: "/usage/", icon: "zap", tone: "ok", sub: "x" },
    },
    {
      name: "a meter named by the label's id",
      ts: {
        id: "tile-cpu",
        label: "CPU",
        value: "36%",
        sub: "Load 5.8",
        meter: { value: 36, thresholds: true, attrs: { "data-meter": "", hidden: true } },
      },
    },
    {
      name: "a meter named by the label's text",
      ts: { label: "Disk", value: "91%", meter: { value: 91, size: "xs", valueText: "91 of 100" } },
    },
    {
      name: "small, not available, in a list",
      ts: {
        label: "worker · CPU",
        value: "n/a",
        sub: "No CPU metric",
        na: true,
        size: "sm",
        tag: "li",
      },
    },
    {
      name: "stale with extras",
      ts: {
        label: "Cost",
        value: "$1",
        stale: true,
        cls: "wide",
        attrs: { "data-stat": "cost", title: "Last read 5 min ago" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(StatTile, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(statTileHTML(c.ts)));
    });
  }

  it("takes markup for the value, the detail and the sparkline in slots", async () => {
    const value = '<span class="num">180</span> ms';
    const sub = "<time>43 min ago</time>";
    const spark = '<svg class="spark" viewBox="0 0 10 2"></svg>';
    const astro = await container.renderToString(StatTile, {
      props: { label: "api · latency", size: "sm" },
      slots: { value, sub, spark },
    });
    const ts = statTileHTML({
      label: "api · latency",
      size: "sm",
      valueHTML: value,
      subHTML: sub,
      sparkHTML: spark,
    });
    expect(normalize(astro)).toBe(normalize(ts));
  });

  it("escapes a text value the way statTileHTML does", async () => {
    const astro = await container.renderToString(StatTile, {
      props: { label: "a", value: "<b>" },
    });
    expect(astro).toContain(esc("<b>"));
  });

  it("refuses a tone that does not exist, like statTileHTML", async () => {
    await expect(
      container.renderToString(StatTile, { props: { label: "a", tone: "good" } }),
    ).rejects.toThrow(/no tone "good"/);
  });
});

describe("StatGroup.astro and statGroupHTML()", () => {
  const tile = statTileHTML({ label: "a", value: 1 });
  const cases: Array<{ name: string; ts: StatGroupOptions }> = [
    { name: "a bare grid", ts: {} },
    { name: "a named grid", ts: { label: "Counts" } },
    { name: "a named strip as a section", ts: { layout: "strip", tag: "section", label: "Usage" } },
    { name: "a flow as a section", ts: { layout: "flow", tag: "section", label: "Metrics" } },
    {
      name: "a list with extras",
      ts: { tag: "ul", id: "kpis", cls: "wide", attrs: { hidden: true } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(StatGroup, {
        props: { ...props, class: cls, ...attrs },
        slots: { default: tile },
      });
      expect(normalize(astro)).toBe(normalize(statGroupHTML([tile], c.ts)));
    });
  }
});
