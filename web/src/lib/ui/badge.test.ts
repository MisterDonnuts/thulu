// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  badgeClasses,
  badgeGlyph,
  badgeHTML,
  dotClasses,
  dotHTML,
  setBadge,
  statusGlyph,
  type BadgeTone,
} from "./badge";

const CLOCK = '<svg class="ico" aria-hidden="true"><use href="#i-clock"/></svg>';
const PULSE = '<span class="dot dot-pulse" aria-hidden="true"></span>';

describe("badgeClasses", () => {
  it("writes badge, then the tone, shape, size, face and edge", () => {
    expect(badgeClasses()).toBe("badge");
    expect(badgeClasses({ tone: "ok" })).toBe("badge badge-ok");
    expect(
      badgeClasses({ tone: "info", shape: "pill", size: "sm", mono: true, edge: "dashed" }),
    ).toBe("badge badge-info badge-pill badge-sm badge-mono badge-dashed");
    expect(badgeClasses({ edge: "solid" })).toBe("badge badge-edge");
    expect(badgeClasses({ tone: "muted", edge: "hollow", cls: "ev-prod" })).toBe(
      "badge badge-muted badge-hollow ev-prod",
    );
  });

  it("writes nothing for the defaults", () => {
    expect(badgeClasses({ tone: "neutral", shape: "rect", size: "md", mono: false })).toBe("badge");
  });

  it("raises a badge over a picture after its edge", () => {
    expect(badgeClasses({ tone: "danger", shape: "pill", raised: true })).toBe(
      "badge badge-danger badge-pill badge-raised",
    );
    expect(badgeClasses({ edge: "hollow", raised: true, cls: "ev-card-pill" })).toBe(
      "badge badge-hollow badge-raised ev-card-pill",
    );
  });

  it("has every tone the sheet styles", () => {
    const tones: BadgeTone[] = ["ok", "warn", "danger", "muted", "accent", "info"];
    for (const tone of tones) expect(badgeClasses({ tone })).toBe(`badge badge-${tone}`);
  });

  it("refuses a tone, shape, size or edge that does not exist", () => {
    expect(() => badgeClasses({ tone: "success" as BadgeTone })).toThrow(/no tone "success"/);
    expect(() => badgeClasses({ shape: "round" as never })).toThrow(/no shape/);
    expect(() => badgeClasses({ size: "lg" as never })).toThrow(/no size/);
    expect(() => badgeClasses({ edge: "dotted" as never })).toThrow(/no edge/);
  });
});

describe("badgeHTML", () => {
  it("keeps the two-argument form every caller writes", () => {
    expect(badgeHTML("idle")).toBe('<span class="badge">idle</span>');
    expect(badgeHTML("ready", "ok")).toBe('<span class="badge badge-ok">ready</span>');
    expect(badgeHTML("default", "accent")).toBe('<span class="badge badge-accent">default</span>');
  });

  it("escapes the label and the tooltip", () => {
    expect(badgeHTML("<img src=x>", "warn", { title: 'a "b"' })).toBe(
      '<span class="badge badge-warn" title="a &quot;b&quot;">&lt;img src=x&gt;</span>',
    );
  });

  it("puts the icon before the label, and the icon wins over a dot", () => {
    expect(badgeHTML("Cloning", "accent", { icon: "i-clock" })).toBe(
      `<span class="badge badge-accent">${CLOCK}Cloning</span>`,
    );
    expect(badgeHTML("Cloning", "accent", { icon: "clock", dot: "ok" })).toBe(
      `<span class="badge badge-accent">${CLOCK}Cloning</span>`,
    );
  });

  it("turns the icon while work runs, and only the icon", () => {
    const html = badgeHTML("5/8", "info", { shape: "pill", icon: "i-refresh", spin: true });
    expect(html).toContain('<svg class="ico badge-spin"');
    expect(html.startsWith('<span class="badge badge-info badge-pill">')).toBe(true);
    expect(badgeHTML("5/8", "info", { spin: true })).toBe(
      '<span class="badge badge-info">5/8</span>',
    );
  });

  it("draws a dot in the badge's colour, in a tone of its own, or breathing", () => {
    expect(badgeHTML("Configured", "ok", { dot: true })).toBe(
      '<span class="badge badge-ok"><span class="dot" aria-hidden="true"></span>Configured</span>',
    );
    expect(badgeHTML("3 up", "neutral", { dot: "ok" })).toBe(
      '<span class="badge"><span class="dot dot-ok" aria-hidden="true"></span>3 up</span>',
    );
    expect(badgeHTML("Running", "info", { shape: "pill", pulse: true })).toBe(
      `<span class="badge badge-info badge-pill">${PULSE}Running</span>`,
    );
  });

  it("writes the id first and the caller's attributes last", () => {
    expect(
      badgeHTML("main", "neutral", {
        id: "b1",
        mono: true,
        size: "sm",
        attrs: { "data-branch": "main", hidden: true },
      }),
    ).toBe('<span id="b1" class="badge badge-sm badge-mono" data-branch="main" hidden>main</span>');
  });
});

describe("dots", () => {
  it("writes dot, the tone and the pulse", () => {
    expect(dotClasses()).toBe("dot");
    expect(dotClasses("ok", true)).toBe("dot dot-ok dot-pulse");
    expect(dotClasses("empty", false, "env-dot")).toBe("dot dot-empty env-dot");
    expect(dotClasses("special")).toBe("dot dot-special");
    expect(() => dotClasses("green" as never)).toThrow(/no tone "green"/);
  });

  it("hides a dot from screen readers unless it carries a label", () => {
    expect(dotHTML("ok")).toBe('<span class="dot dot-ok" aria-hidden="true"></span>');
    expect(dotHTML("danger", { label: "Down" })).toBe(
      '<span class="dot dot-danger" role="img" aria-label="Down" title="Down"></span>',
    );
  });

  it("takes an id and attributes", () => {
    expect(dotHTML("neutral", { id: "live-dot", attrs: { hidden: true } })).toBe(
      '<span id="live-dot" class="dot" aria-hidden="true" hidden></span>',
    );
  });

  it("writes the run glyph: the icon, or a breathing dot", () => {
    expect(statusGlyph("i-clock")).toBe(CLOCK);
    expect(statusGlyph()).toBe(PULSE);
    expect(statusGlyph("")).toBe(PULSE);
    expect(statusGlyph(undefined, "info")).toBe(
      '<span class="dot dot-info dot-pulse" aria-hidden="true"></span>',
    );
  });

  it("writes no glyph for a plain badge", () => {
    expect(badgeGlyph({})).toBe("");
    expect(badgeGlyph({ dot: false })).toBe("");
  });
});

describe("setBadge", () => {
  it("repaints the classes, glyph, label and tooltip and keeps the id", () => {
    const host = document.createElement("div");
    host.innerHTML = badgeHTML("On", "ok", { id: "d-state", title: "since May" });
    const el = host.firstElementChild as HTMLElement;

    setBadge(el, "Off <b>", "muted", { dot: true });
    expect(el.outerHTML).toBe(
      '<span id="d-state" class="badge badge-muted"><span class="dot" aria-hidden="true"></span>Off &lt;b&gt;</span>',
    );

    setBadge(el, "On", "ok", { title: "now" });
    expect(el.outerHTML).toBe('<span id="d-state" class="badge badge-ok" title="now">On</span>');
  });

  it("never leaves a toneless double class behind", () => {
    const el = document.createElement("span");
    el.className = "badge badge-ok";
    setBadge(el, "Off");
    expect(el.className).toBe("badge");
  });
});
