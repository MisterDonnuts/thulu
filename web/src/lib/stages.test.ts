import { describe, expect, it } from "vitest";

import { movesBack, stagePillHTML, type Stage } from "./stages";

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

describe("movesBack", () => {
  const col = (id: string, position: number, virtual = false) =>
    ({ id, position, virtual }) as Stage;
  const board = [col("backlog", 10), col("build", 20), col("review", 30), col("archive", 5, true)];

  it("is back only toward an earlier column of the board", () => {
    expect(movesBack(board, "review", "build")).toBe(true);
    expect(movesBack(board, "build", "review")).toBe(false);
    expect(movesBack(board, "build", "build")).toBe(false);
  });

  it("never counts a virtual or unknown column as back", () => {
    expect(movesBack(board, "review", "archive")).toBe(false);
    expect(movesBack(board, "review", "gone")).toBe(false);
  });
});
