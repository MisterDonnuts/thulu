// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  meterA11y,
  meterClasses,
  meterHTML,
  meterTone,
  progressBarClasses,
  progressBarHTML,
  progressHTML,
  progressSegments,
  progressText,
  progressTextHTML,
  setMeter,
} from "./progress";

describe("progressBarClasses", () => {
  it("writes seg-progress, then the size, then split", () => {
    expect(progressBarClasses()).toBe("seg-progress");
    expect(progressBarClasses({ size: "xs", split: true, cls: "card-bar" })).toBe(
      "seg-progress seg-progress-xs seg-progress-split card-bar",
    );
    expect(progressBarClasses({ size: "md" })).toBe("seg-progress seg-progress-md");
  });

  it("refuses a size that does not exist", () => {
    // @ts-expect-error: not a size
    expect(() => progressBarClasses({ size: "lg" })).toThrow(/no size "lg"/);
  });
});

describe("progressSegments", () => {
  it("sizes each segment as a share of the total and drops the empty ones", () => {
    const parts = progressSegments(
      [
        { value: 2, tone: "ok", label: "2 passed" },
        { value: 0, tone: "warn" },
        { value: 1, tone: "danger" },
      ],
      3,
    );
    expect(parts).toEqual([
      { cls: "seg-progress-seg", tone: "ok", style: "flex-basis:66.67%", title: "2 passed" },
      { cls: "seg-progress-seg", tone: "danger", style: "flex-basis:33.33%", title: undefined },
    ]);
  });

  it("leaves what the segments do not cover as one track segment", () => {
    const parts = progressSegments([{ value: 1, tone: "ok" }], 4);
    expect(parts.map((p) => [p.tone, p.style])).toEqual([
      ["ok", "flex-basis:25%"],
      ["track", "flex-basis:75%"],
    ]);
  });

  it("outlines work under way and never grows past the sum", () => {
    const parts = progressSegments([{ value: 3, tone: "info", outline: true }], 2);
    expect(parts).toHaveLength(1);
    expect(parts[0].cls).toBe("seg-progress-seg seg-progress-outline");
    expect(parts[0].style).toBe("flex-basis:100%");
  });

  it("has nothing to draw for an empty set", () => {
    expect(progressSegments([], 0)).toEqual([]);
  });
});

describe("progressBarHTML", () => {
  it("names the bar after its segments", () => {
    const html = progressBarHTML(
      [
        { value: 2, tone: "ok", label: "2 passed" },
        { value: 1, tone: "danger", label: "1 failed" },
      ],
      { total: 5 },
    );
    expect(html).toBe(
      '<span class="seg-progress" role="img" aria-label="2 passed, 1 failed">' +
        '<span class="seg-progress-seg" data-tone="ok" style="flex-basis:40%" title="2 passed"></span>' +
        '<span class="seg-progress-seg" data-tone="danger" style="flex-basis:20%" title="1 failed"></span>' +
        '<span class="seg-progress-seg" data-tone="track" style="flex-basis:40%"></span></span>',
    );
  });

  it("takes an explicit name, hides when decorative, and escapes", () => {
    expect(progressBarHTML([{ value: 1, tone: "ok" }], { label: 'a "b" <c>' })).toContain(
      'aria-label="a &quot;b&quot; &lt;c&gt;"',
    );
    const deco = progressBarHTML([{ value: 1, tone: "ok", label: "x" }], { decorative: true });
    expect(deco).toContain('aria-hidden="true"');
    expect(deco).not.toContain("role=");
  });

  it("draws an empty track with a name for an empty set", () => {
    expect(progressBarHTML([])).toBe(
      '<span class="seg-progress" role="img" aria-label="No items"></span>',
    );
  });

  it("carries extra attributes", () => {
    expect(progressBarHTML([], { attrs: { id: "t-progress" } })).toContain('id="t-progress"');
  });
});

describe("meter", () => {
  it("turns warn at 75% and danger at 90% unless told otherwise", () => {
    expect(meterTone(74.9)).toBe("accent");
    expect(meterTone(75)).toBe("warn");
    expect(meterTone(90)).toBe("danger");
    expect(meterTone(50, { warn: 40, danger: 80 }, "ok")).toBe("warn");
    expect(meterTone(10, undefined, "ok")).toBe("ok");
  });

  it("writes meter-bar, its size, then inline", () => {
    expect(meterClasses()).toBe("meter-bar");
    expect(meterClasses({ size: "xs", inline: true })).toBe(
      "meter-bar meter-bar-xs meter-bar-inline",
    );
  });

  it("needs a name", () => {
    expect(() => meterA11y({ value: 1 })).toThrow(/needs a label/);
    expect(meterA11y({ value: 1, labelledBy: "x" })["aria-labelledby"]).toBe("x");
  });

  it("writes role=meter with the value clamped and the tone from the thresholds", () => {
    expect(meterHTML(120, { label: "Disk", thresholds: true })).toBe(
      '<span class="meter-bar" data-tone="danger" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="100" aria-label="Disk">' +
        '<span class="meter-bar-fill" style="width:100%"></span></span>',
    );
    const html = meterHTML(42.4, {
      label: "Runs",
      valueText: "424 of 1000",
      tone: "ok",
      size: "md",
    });
    expect(html).toContain('class="meter-bar meter-bar-md"');
    expect(html).toContain('data-tone="ok"');
    expect(html).toContain('aria-valuenow="42"');
    expect(html).toContain('aria-valuetext="424 of 1000"');
    expect(html).toContain('style="width:42.4%"');
    expect(meterHTML(-5, { label: "x" })).toContain('style="width:0%"');
  });

  it("moves a drawn meter without rebuilding it", () => {
    const host = document.createElement("div");
    host.innerHTML = meterHTML(10, { label: "CPU", thresholds: true });
    const m = host.firstElementChild as HTMLElement;
    setMeter(m, 80, { thresholds: true, valueText: "80 of 100" });
    expect(m.dataset.tone).toBe("warn");
    expect(m.getAttribute("aria-valuenow")).toBe("80");
    expect(m.getAttribute("aria-valuetext")).toBe("80 of 100");
    expect((m.firstElementChild as HTMLElement).style.width).toBe("80%");
    setMeter(m, 20);
    expect(m.dataset.tone).toBe("accent");
    expect(m.hasAttribute("aria-valuetext")).toBe(false);
  });
});

describe("progressHTML", () => {
  it("says the count one way, short or long", () => {
    expect(progressText({ done: 1, total: 4 })).toBe("1 of 4");
    expect(progressText({ done: 1, total: 4 }, "long")).toBe("1 of 4 done");
  });

  it("writes a hidden bar and the text beside it", () => {
    const html = progressHTML({ done: 1, total: 4 }, { text: "long", cls: "card-progress" });
    expect(html).toBe(
      '<span class="progress-line card-progress">' +
        '<span class="seg-progress seg-progress-xs" aria-hidden="true">' +
        '<span class="seg-progress-seg" data-tone="ok" style="flex-basis:25%"></span>' +
        '<span class="seg-progress-seg" data-tone="track" style="flex-basis:75%"></span></span>' +
        '<span class="progress-line-text">1 of 4 done</span></span>',
    );
  });

  it("takes the caller's segments and writes nothing for an empty set", () => {
    const html = progressHTML({
      done: 1,
      total: 3,
      segments: [
        { value: 1, tone: "ok" },
        { value: 1, tone: "info", outline: true },
      ],
    });
    expect(html).toContain("seg-progress-outline");
    expect(progressHTML({ done: 0, total: 0 })).toBe("");
  });
});

describe("progressTextHTML", () => {
  it("writes the count alone in the look it has beside a bar", () => {
    expect(progressTextHTML({ done: 2, total: 3 }, { text: "long", cls: "sec-count" })).toBe(
      '<span class="progress-line-text sec-count">2 of 3 done</span>',
    );
  });

  it("writes an empty span with an id for a script to fill", () => {
    expect(progressTextHTML(null, { id: "st-progress" })).toBe(
      '<span class="progress-line-text" id="st-progress"></span>',
    );
    expect(progressTextHTML({ done: 0, total: 0 })).toBe(
      '<span class="progress-line-text"></span>',
    );
  });
});
