import { describe, expect, it } from "vitest";

import { tableHeadHTML, tableRowHTML } from "./ui/table";
import { DIM_TABLES, dimColumns, skillColumns, toolColumns, topTaskColumns } from "./usage-tables";

describe("dimColumns", () => {
  it("puts the key first and one number column per measure", () => {
    for (const id of Object.keys(DIM_TABLES) as Array<keyof typeof DIM_TABLES>) {
      const cols = dimColumns(id);
      expect(cols).toHaveLength(DIM_TABLES[id].metrics.length + 1);
      expect(cols[0].num).toBeUndefined();
      expect(cols.slice(1).every((c) => c.num)).toBe(true);
    }
  });

  it("names the columns the way the head always did", () => {
    expect(dimColumns("t-model").map((c) => c.label)).toEqual([
      "Model",
      "Sessions",
      "Tokens",
      "Cost",
      "$/sess",
      "p95 dur",
    ]);
  });

  it("names every cell of a stacked row after its column", () => {
    const row = tableRowHTML(
      dimColumns("t-project"),
      { cells: ["p", "1", "2", "$3"] },
      { stack: true },
    );
    expect(row).toBe(
      '<tr><td data-label="Project">p</td><td class="num" data-label="Sessions">1</td>' +
        '<td class="num" data-label="Tokens">2</td><td class="num" data-label="Cost">$3</td></tr>',
    );
  });
});

describe("tool, skill and task columns", () => {
  it("keep the order of the values each renderer writes", () => {
    expect(toolColumns().map((c) => c.label)).toEqual([
      "Tool",
      "Kind",
      "Calls",
      "Sessions",
      "Result tokens",
      "Re-read",
      "Cost",
    ]);
    expect(skillColumns()).toHaveLength(5);
  });

  it("hide the name of the link column but keep it for screen readers", () => {
    const cols = topTaskColumns();
    expect(cols).toHaveLength(7);
    expect(tableHeadHTML(cols)).toContain('<span class="visually-hidden">Open</span>');
  });
});
