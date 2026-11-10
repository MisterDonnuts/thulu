// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  segmentedCheck,
  segmentedClasses,
  segmentedEl,
  segmentedHTML,
  segmentedLabelClasses,
  segmentedValue,
  setSegmentedValue,
  type SegmentedOption,
} from "./segmented";

const RANGES: SegmentedOption[] = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
];

const VIEWS: SegmentedOption[] = [
  { value: "grid", label: "Grid", icon: "columns" },
  { value: "list", label: "List", icon: "list" },
];

describe("segmentedClasses", () => {
  it("writes segmented, then the variants, then the extras", () => {
    expect(segmentedClasses()).toBe("segmented");
    expect(segmentedClasses({ size: "md" })).toBe("segmented");
    expect(segmentedClasses({ size: "sm", iconOnly: true })).toBe(
      "segmented segmented-sm segmented-icons",
    );
    expect(segmentedClasses({ cards: true, fill: true, cls: "plan-pick" })).toBe(
      "segmented segmented-cards segmented-fill plan-pick",
    );
  });

  it("refuses a size it does not draw", () => {
    // @ts-expect-error a size the component does not draw
    expect(() => segmentedClasses({ size: "lg" })).toThrow(/no size "lg"/);
  });

  it("refuses icons alone on cards", () => {
    expect(() => segmentedClasses({ cards: true, iconOnly: true })).toThrow(/not icons alone/);
  });
});

describe("segmentedCheck", () => {
  it("takes two or more distinct options", () => {
    expect(() => segmentedCheck(RANGES)).not.toThrow();
  });

  it("refuses a single option: there is nothing to choose", () => {
    expect(() => segmentedCheck([RANGES[0]])).toThrow(/two options/);
  });

  it("refuses the same value twice", () => {
    expect(() => segmentedCheck([RANGES[0], { value: "15m", label: "again" }])).toThrow(
      /"15m" twice/,
    );
  });

  it("refuses an icon-only option without a glyph", () => {
    expect(() => segmentedCheck([VIEWS[0], { value: "x", label: "X" }], true)).toThrow(
      /"x" has no icon/,
    );
  });
});

describe("segmentedLabelClasses", () => {
  it("hides the words from sight in an icon-only group", () => {
    expect(segmentedLabelClasses()).toBe("segmented-label");
    expect(segmentedLabelClasses(true)).toBe("segmented-label visually-hidden");
  });
});

describe("segmentedHTML", () => {
  it("writes a named radiogroup with one radio per option, the current one checked", () => {
    const el = segmentedEl("range", RANGES, "1h", { size: "sm", label: "Range" });
    expect(el.className).toBe("segmented segmented-sm");
    expect(el.getAttribute("role")).toBe("radiogroup");
    expect(el.getAttribute("aria-label")).toBe("Range");
    const radios = [...el.querySelectorAll<HTMLInputElement>("input")];
    expect(radios.map((r) => [r.type, r.name, r.value, r.checked])).toEqual([
      ["radio", "range", "15m", false],
      ["radio", "range", "1h", true],
      ["radio", "range", "24h", false],
    ]);
    expect([...el.querySelectorAll("label.segmented-opt")].map((l) => l.textContent)).toEqual([
      "15m",
      "1h",
      "24h",
    ]);
  });

  it("checks nothing when the value matches no option", () => {
    const el = segmentedEl("range", RANGES, "7d");
    expect(el.querySelector("input:checked")).toBeNull();
    expect(segmentedEl("range", RANGES).querySelector("input:checked")).toBeNull();
  });

  it("escapes the labels, the values and the attributes", () => {
    const html = segmentedHTML(
      "n",
      [
        { value: '"a"', label: "<b>A</b>", attrs: { title: "x & y" } },
        { value: "b", label: "B" },
      ],
      '"a"',
    );
    expect(html).not.toContain("<b>");
    expect(html).toContain("&lt;b&gt;A&lt;/b&gt;");
    expect(html).toContain('value="&quot;a&quot;"');
    expect(html).toContain('title="x &amp; y"');
  });

  it("draws glyphs and keeps the words as hidden names and tooltips with iconOnly", () => {
    const el = segmentedEl("view", VIEWS, "list", { iconOnly: true, label: "View" });
    const opts = [...el.querySelectorAll<HTMLLabelElement>(".segmented-opt")];
    expect(opts.map((o) => o.title)).toEqual(["Grid", "List"]);
    expect(opts[0].querySelector("svg.segmented-ico use")?.getAttribute("href")).toBe("#i-columns");
    expect(opts[0].querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(opts[0].querySelector(".segmented-label")?.className).toBe(
      "segmented-label visually-hidden",
    );
    expect(opts[1].querySelector("input")?.labels?.[0]?.textContent).toBe("List");
  });

  it("keeps the tooltip off options that show their words", () => {
    const el = segmentedEl("view", VIEWS, "grid");
    expect(el.querySelector(".segmented-opt")?.hasAttribute("title")).toBe(false);
  });

  it("turns every option off, or one", () => {
    const all = segmentedEl("r", RANGES, "1h", { disabled: true });
    expect([...all.querySelectorAll("input")].every((r) => r.disabled)).toBe(true);
    const one = segmentedEl("r", [RANGES[0], { ...RANGES[1], disabled: true }], "15m");
    expect([...one.querySelectorAll("input")].map((r) => r.disabled)).toEqual([false, true]);
  });

  it("names the group by a heading and a description, and takes an id and hooks", () => {
    const el = segmentedEl(
      "wip",
      [
        { value: "off", label: "Off", inputAttrs: { "data-k": "off" } },
        { value: "warn", label: "Warn" },
      ],
      "off",
      {
        id: "ce-wip",
        labelledBy: "ce-sec-wip",
        describedBy: "ce-wip-d",
        attrs: { "data-x": "1" },
      },
    );
    expect(el.id).toBe("ce-wip");
    expect(el.getAttribute("aria-labelledby")).toBe("ce-sec-wip");
    expect(el.getAttribute("aria-describedby")).toBe("ce-wip-d");
    expect(el.dataset.x).toBe("1");
    expect(el.querySelector("input")?.dataset.k).toBe("off");
    expect(el.hasAttribute("aria-label")).toBe(false);
  });

  it("throws on options both forms refuse", () => {
    expect(() => segmentedHTML("n", [RANGES[0]])).toThrow(/two options/);
    expect(() => segmentedHTML("n", RANGES, "1h", { iconOnly: true })).toThrow(/has no icon/);
  });
});

describe("segmentedValue and setSegmentedValue", () => {
  it("reads the chosen value, or null", () => {
    expect(segmentedValue(segmentedEl("r", RANGES, "24h"))).toBe("24h");
    expect(segmentedValue(segmentedEl("r", RANGES))).toBeNull();
  });

  it("chooses an option without firing change, and refuses an unknown value", () => {
    const el = segmentedEl("r", RANGES, "15m");
    let changes = 0;
    el.addEventListener("change", () => changes++);
    expect(setSegmentedValue(el, "24h")).toBe(true);
    expect(segmentedValue(el)).toBe("24h");
    expect(setSegmentedValue(el, "7d")).toBe(false);
    expect(segmentedValue(el)).toBe("24h");
    expect(changes).toBe(0);
  });

  it("reports a click on an option as a change on the group", () => {
    const el = segmentedEl("r", RANGES, "15m");
    document.body.append(el);
    const seen: string[] = [];
    el.addEventListener("change", (e) => seen.push((e.target as HTMLInputElement).value));
    el.querySelectorAll<HTMLLabelElement>(".segmented-opt")[2].click();
    expect(seen).toEqual(["24h"]);
    expect(segmentedValue(el)).toBe("24h");
    el.remove();
  });
});
