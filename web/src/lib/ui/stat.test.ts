// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  setStat,
  statClasses,
  statGroupClasses,
  statGroupHTML,
  statIds,
  statTileHTML,
  statTone,
} from "./stat";

describe("statClasses", () => {
  it("writes stat, then the size, link, na and the extras", () => {
    expect(statClasses()).toBe("stat");
    expect(statClasses({ size: "sm", link: true, na: true, cls: "sig" })).toBe(
      "stat stat-sm stat-link stat-na sig",
    );
  });

  it("refuses a size that does not exist", () => {
    // @ts-expect-error: not a size
    expect(() => statClasses({ size: "lg" })).toThrow(/no size "lg"/);
  });
});

describe("statTone", () => {
  it("leaves neutral out and keeps the rest", () => {
    expect(statTone()).toBeUndefined();
    expect(statTone("neutral")).toBeUndefined();
    expect(statTone("danger")).toBe("danger");
  });

  it("refuses a tone that does not exist", () => {
    // @ts-expect-error: not a tone
    expect(() => statTone("good")).toThrow(/no tone "good"/);
  });
});

describe("statIds", () => {
  it("names the label after the tile, and nothing without an id", () => {
    expect(statIds("tile-cpu")).toEqual({ label: "tile-cpu-label" });
    expect(statIds(undefined)).toEqual({});
  });
});

describe("statTileHTML", () => {
  it("writes label, value and detail in that order, escaped", () => {
    expect(statTileHTML({ label: "Open <tasks>", value: 5, sub: "2 running & 1 queued" })).toBe(
      '<div class="stat"><span class="stat-label">Open &lt;tasks&gt;</span>' +
        '<span class="stat-value">5</span>' +
        '<span class="stat-sub">2 running &amp; 1 queued</span></div>',
    );
  });

  it("writes a zero, and an empty value when there is none", () => {
    expect(statTileHTML({ label: "Errors", value: 0 })).toContain(
      '<span class="stat-value">0</span>',
    );
    expect(statTileHTML({ label: "Errors" })).toContain('<span class="stat-value"></span>');
  });

  it("takes markup for the value and the detail when no text is given", () => {
    const html = statTileHTML({
      label: "Cost",
      valueHTML: '<span class="cost">$1</span>',
      subHTML: "<b>up</b>",
    });
    expect(html).toContain('<span class="stat-value"><span class="cost">$1</span></span>');
    expect(html).toContain('<span class="stat-sub"><b>up</b></span>');
  });

  it("is a link as a whole with an href, whatever tag was asked", () => {
    const html = statTileHTML({ label: "Open", value: 1, href: "/tasks/?a=1&b=2", tag: "li" });
    expect(html.startsWith('<a class="stat stat-link" href="/tasks/?a=1&amp;b=2">')).toBe(true);
    expect(html.endsWith("</a>")).toBe(true);
  });

  it("puts the glyph box first", () => {
    const html = statTileHTML({ label: "Open", value: 1, icon: "board" });
    expect(html).toMatch(
      /^<div class="stat"><span class="stat-icon"><svg [^>]*><use href="[^"]*i-board"/,
    );
  });

  it("carries the tone as data-tone, never for neutral", () => {
    expect(statTileHTML({ label: "x", value: 1, tone: "ok" })).toContain('data-tone="ok"');
    expect(statTileHTML({ label: "x", value: 1, tone: "neutral" })).not.toContain("data-tone");
  });

  it("names its meter by the label on screen when the tile has an id", () => {
    const html = statTileHTML({
      id: "tile-cpu",
      label: "CPU",
      value: "36%",
      meter: { value: 36, thresholds: true, attrs: { "data-meter": "" } },
    });
    expect(html).toContain('<span class="stat-label" id="tile-cpu-label">CPU</span>');
    expect(html).toContain('aria-labelledby="tile-cpu-label"');
    expect(html).toContain('class="meter-bar stat-meter"');
    expect(html).toContain("data-meter");
  });

  it("names its meter with the label's text without an id", () => {
    const html = statTileHTML({ label: "Disk", value: "91%", meter: { value: 91 } });
    expect(html).toContain('aria-label="Disk"');
    expect(html).not.toContain("aria-labelledby");
  });

  it("writes the meter, then the sparkline, then the detail", () => {
    const html = statTileHTML({
      label: "api · errors",
      value: 0,
      sparkHTML: '<svg class="spark"></svg>',
      sub: "43 min ago",
      size: "sm",
    });
    expect(html).toBe(
      '<div class="stat stat-sm"><span class="stat-label">api · errors</span>' +
        '<span class="stat-value">0</span>' +
        '<span class="stat-spark"><svg class="spark"></svg></span>' +
        '<span class="stat-sub">43 min ago</span></div>',
    );
  });

  it("marks a stale tile and says so in a badge", () => {
    const html = statTileHTML({ label: "Cost", value: "$1", stale: true });
    expect(html).toContain('<div class="stat" data-stale>');
    expect(html).toContain('class="badge badge-warn badge-pill badge-sm stat-stale"');
    expect(html).toContain(">stale</span>");
  });

  it("writes a figure that does not exist with its own class", () => {
    expect(statTileHTML({ label: "worker · CPU", value: "n/a", na: true })).toContain(
      'class="stat stat-na"',
    );
  });

  it("writes the id, the extras and the caller's attributes", () => {
    const html = statTileHTML({
      label: "x",
      value: 1,
      id: "m-cost",
      cls: "wide",
      attrs: { hidden: true, "data-stat": "cost" },
    });
    expect(html).toContain('<div id="m-cost" class="stat wide" hidden data-stat="cost">');
  });
});

describe("statGroupClasses", () => {
  it("writes the layout, then the extras", () => {
    expect(statGroupClasses()).toBe("stat-grid");
    expect(statGroupClasses({ layout: "strip", cls: "ws" })).toBe("stat-strip ws");
    expect(statGroupClasses({ layout: "flow" })).toBe("stat-flow");
  });

  it("refuses a layout that does not exist", () => {
    // @ts-expect-error: not a layout
    expect(() => statGroupClasses({ layout: "row" })).toThrow(/no layout "row"/);
  });
});

describe("statGroupHTML", () => {
  const a = statTileHTML({ label: "a", value: 1 });
  const b = statTileHTML({ label: "b", value: 2 });

  it("holds the tiles in a grid, named as a group", () => {
    expect(statGroupHTML([a, b], { label: "Counts" })).toBe(
      `<div class="stat-grid" role="group" aria-label="Counts">${a}${b}</div>`,
    );
  });

  it("does not give a section a role: it is already a region", () => {
    expect(statGroupHTML(a, { tag: "section", label: "Usage", layout: "strip" })).toBe(
      `<section class="stat-strip" aria-label="Usage">${a}</section>`,
    );
  });

  it("writes no role without a label", () => {
    expect(statGroupHTML([a])).toBe(`<div class="stat-grid">${a}</div>`);
  });
});

describe("setStat", () => {
  function tile(html: string): HTMLElement {
    const host = document.createElement("div");
    host.innerHTML = html;
    return host.firstElementChild as HTMLElement;
  }

  it("moves the value and the detail", () => {
    const el = tile(statTileHTML({ label: "Open", value: 1, sub: "old" }));
    setStat(el, { value: 7, sub: "3 running" });
    expect(el.querySelector(".stat-value")?.textContent).toBe("7");
    expect(el.querySelector(".stat-sub")?.textContent).toBe("3 running");
  });

  it("adds a detail line the tile did not have, and hides an emptied one", () => {
    const el = tile(statTileHTML({ label: "Open", value: 1 }));
    setStat(el, { sub: "new" });
    const sub = el.querySelector<HTMLElement>(".stat-sub");
    expect(sub?.textContent).toBe("new");
    setStat(el, { sub: "" });
    expect(sub?.hidden).toBe(true);
  });

  it("sets and clears the tone", () => {
    const el = tile(statTileHTML({ label: "Cost", value: 1 }));
    setStat(el, { tone: "danger" });
    expect(el.dataset.tone).toBe("danger");
    setStat(el, { tone: "neutral" });
    expect(el.hasAttribute("data-tone")).toBe(false);
  });

  it("marks and unmarks a stale tile with one badge, the detail before it", () => {
    const el = tile(statTileHTML({ label: "Cost", value: 1 }));
    setStat(el, { stale: true });
    setStat(el, { stale: true });
    expect(el.hasAttribute("data-stale")).toBe(true);
    expect(el.querySelectorAll(".stat-stale")).toHaveLength(1);
    setStat(el, { sub: "from 5 min ago" });
    expect(el.lastElementChild?.classList.contains("stat-stale")).toBe(true);
    setStat(el, { stale: false });
    expect(el.hasAttribute("data-stale")).toBe(false);
    expect(el.querySelector(".stat-stale")).toBeNull();
  });

  it("leaves alone what the patch does not name", () => {
    const el = tile(statTileHTML({ label: "Cost", value: 4, sub: "s", tone: "ok" }));
    setStat(el, {});
    expect(el.querySelector(".stat-value")?.textContent).toBe("4");
    expect(el.dataset.tone).toBe("ok");
  });
});
