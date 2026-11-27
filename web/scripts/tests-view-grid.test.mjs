import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The Tests tab's case list is a grid with one column per Testing round shown. repeat(0, ...) is
// invalid CSS, so a list with no round column (cases that never ran, or a narrow drawer) needs a
// template of its own: with the invalid one the whole declaration drops and every cell of a row
// stacks in one column.
const web = join(dirname(fileURLToPath(import.meta.url)), "..");
const css = readFileSync(join(web, "src/styles/task-view.css"), "utf8").replace(
  /\/\*[\s\S]*?\*\//g,
  "",
);

function colsOf(selector) {
  for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = m[1].split(",").map((x) => x.trim());
    if (!selectors.includes(selector)) continue;
    const cols = m[2].match(/--tv-cols:\s*([^;]+);/);
    if (cols) return cols[1].trim();
  }
  return undefined;
}

describe("task-view.css case list grid", () => {
  it("gives a list with no round column a template without repeat()", () => {
    const zero = colsOf('.tv-list[data-rounds="0"]');
    expect(zero).toBeDefined();
    expect(zero).not.toMatch(/repeat\(/);
  });

  it("sizes the round columns from --tv-rn otherwise", () => {
    expect(colsOf(".tv-list")).toMatch(/repeat\(var\(--tv-rn/);
  });
});
