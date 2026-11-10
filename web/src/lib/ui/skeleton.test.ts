import { describe, expect, it } from "vitest";

import { skeletonClasses, skeletonHTML, skeletonLineCount } from "./skeleton";

describe("skeletonClasses", () => {
  it("writes skeleton, the shape, then the extras", () => {
    expect(skeletonClasses()).toBe("skeleton skeleton-line");
    expect(skeletonClasses("chip", "bs-chip-wait")).toBe("skeleton skeleton-chip bs-chip-wait");
  });

  it("throws on a shape that does not exist", () => {
    expect(() => skeletonClasses("circle" as never)).toThrow(/no shape "circle"/);
  });
});

describe("skeletonLineCount", () => {
  it("draws one bar unless a line skeleton asks for more", () => {
    expect(skeletonLineCount("line", undefined)).toBe(1);
    expect(skeletonLineCount("line", 0)).toBe(1);
    expect(skeletonLineCount("line", 3)).toBe(3);
    expect(skeletonLineCount("line", 2.7)).toBe(2);
    expect(skeletonLineCount("block", 1)).toBe(1);
  });

  it("refuses lines on a block or a chip", () => {
    expect(() => skeletonLineCount("block", 3)).toThrow(/only a line skeleton/);
  });
});

describe("skeletonHTML", () => {
  it("writes one hidden span by default", () => {
    expect(skeletonHTML()).toBe('<span class="skeleton skeleton-line" aria-hidden="true"></span>');
  });

  it("draws the shape on the tag it is given, with the caller's attributes", () => {
    expect(skeletonHTML({ shape: "chip", tag: "li" })).toBe(
      '<li class="skeleton skeleton-chip" aria-hidden="true"></li>',
    );
    expect(
      skeletonHTML({ shape: "block", tag: "div", cls: "bs-card", attrs: { "data-wait": 1 } }),
    ).toBe('<div class="skeleton skeleton-block bs-card" aria-hidden="true" data-wait="1"></div>');
  });

  it("stacks a paragraph of bars inside one hidden wrapper", () => {
    expect(skeletonHTML({ lines: 3, cls: "desc" })).toBe(
      '<span class="skeleton-lines desc" aria-hidden="true">' +
        '<span class="skeleton skeleton-line"></span>'.repeat(3) +
        "</span>",
    );
  });

  it("throws on a tag it does not draw", () => {
    expect(() => skeletonHTML({ tag: "td" as never })).toThrow(/no tag "td"/);
  });
});
