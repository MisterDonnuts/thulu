import { describe, expect, it } from "vitest";

import { stagePillHTML } from "./stages";

describe("stagePillHTML", () => {
  it("tints the tag with the board's colour for the stage and adds the dot", () => {
    expect(stagePillHTML("review", { label: "Review", color: "#a78bfa" })).toBe(
      '<span class="tag tag-tint" style="--tag-color:#a78bfa"><span class="tag-dot" aria-hidden="true"></span><span class="tag-label">Review</span></span>',
    );
  });

  it("shows the id in grey for a stage the board no longer has", () => {
    expect(stagePillHTML("gone", undefined)).toBe(
      '<span class="tag"><span class="tag-dot" aria-hidden="true"></span><span class="tag-label">gone</span></span>',
    );
  });

  it("ignores a colour that is not a hex", () => {
    expect(stagePillHTML("x", { label: "X", color: "red;}" })).not.toContain("style=");
  });

  it("is the drawer head's picker button with button", () => {
    const html = stagePillHTML(
      "review",
      { label: "Review", color: "#a78bfa" },
      { button: true, id: "dr-stage", attrs: { "aria-haspopup": "listbox" } },
    );
    expect(html).toMatch(
      /^<button id="dr-stage" class="tag tag-tint" style="--tag-color:#a78bfa" type="button" aria-haspopup="listbox">/,
    );
  });
});
