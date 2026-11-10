import { describe, expect, it } from "vitest";

import {
  setTableSort,
  tableCellHTML,
  tableClasses,
  tableColumnClasses,
  tableHeadHTML,
  tableHTML,
  tableRowHTML,
  tableSpan,
  type TableColumn,
} from "./table";

const cols: TableColumn[] = [
  { label: "Name" },
  { label: "CPU", num: true, sort: "descending", key: "cpu" },
  { label: "Actions", hideLabel: true, width: "shrink" },
];

describe("tableClasses", () => {
  it("is list-table, then the variants, then extras", () => {
    expect(tableClasses()).toBe("list-table");
    expect(tableClasses({ top: true, compact: true, stack: true, noHead: true, cls: "pods" })).toBe(
      "list-table list-table-top list-table-compact list-table-stack list-table-no-head pods",
    );
  });

  it("gives a column's cells their alignment, width and font, never mono to the head", () => {
    const col: TableColumn = { label: "Id", num: true, mono: true, width: "fill", cls: "c" };
    expect(tableColumnClasses(col)).toBe("num cell-fill c");
    expect(tableColumnClasses(col, true)).toBe("num mono cell-fill c");
    expect(tableColumnClasses({ label: "x" })).toBeUndefined();
    expect(tableColumnClasses({ label: "Task", width: "title" })).toBe("cell-title");
  });
});

describe("tableHeadHTML", () => {
  it("writes scope, a sort button and a hidden label", () => {
    expect(tableHeadHTML(cols)).toBe(
      '<thead><tr><th scope="col">Name</th>' +
        '<th scope="col" class="num" data-sort="cpu" aria-sort="descending"><button type="button">CPU</button></th>' +
        '<th scope="col" class="cell-shrink"><span class="visually-hidden">Actions</span></th></tr></thead>',
    );
  });

  it("adds a caret column to an expandable table", () => {
    expect(tableHeadHTML([{ label: "A" }], { expandable: true })).toBe(
      '<thead><tr><th scope="col" class="cell-caret"><span class="visually-hidden">Details</span></th>' +
        '<th scope="col">A</th></tr></thead>',
    );
  });

  it("refuses a sort order that does not exist", () => {
    expect(() => tableHeadHTML([{ label: "A", sort: "up" as never }])).toThrow(/no sort "up"/);
  });
});

describe("tableCellHTML", () => {
  it("escapes text, keeps html, and adds the second line", () => {
    expect(tableCellHTML({ label: "N" }, "<b>")).toBe("<td>&lt;b&gt;</td>");
    expect(tableCellHTML({ label: "N" }, { html: "<a>x</a>", sub: "a & b" })).toBe(
      '<td><a>x</a><span class="cell-sub">a &amp; b</span></td>',
    );
    expect(tableCellHTML({ label: "N" }, { text: "x", subHTML: '<a href="/t/">t</a>' })).toBe(
      '<td>x<span class="cell-sub"><a href="/t/">t</a></span></td>',
    );
    expect(tableCellHTML({ label: "N" }, { text: "x", sub: "s", subHTML: "<b>no</b>" })).toBe(
      '<td>x<span class="cell-sub">s</span></td>',
    );
    expect(tableCellHTML({ label: "N" }, null)).toBe("<td></td>");
    expect(tableCellHTML({ label: "N", num: true }, 0)).toBe('<td class="num">0</td>');
  });

  it("names the cell after its column in a stacking table", () => {
    expect(tableCellHTML({ label: "CPU" }, "7%", { stack: true })).toBe(
      '<td data-label="CPU">7%</td>',
    );
    expect(tableCellHTML({ label: "Act", hideLabel: true }, "", { stack: true })).toBe("<td></td>");
  });

  it("draws actions as sm buttons on one line", () => {
    const html = tableCellHTML({ label: "A" }, { actions: [{ label: "Retry", size: "md" }] });
    expect(html).toBe(
      '<td class="actions-cell"><span class="cell-actions"><button class="btn btn-sm" type="button">Retry</button></span></td>',
    );
  });
});

describe("tableRowHTML", () => {
  it("marks tone, dim and id, and fills missing cells", () => {
    expect(
      tableRowHTML(cols, { cells: ["a"], tone: "danger", dim: true, id: "r1", cls: "dec-row" }),
    ).toBe(
      '<tr class="list-table-dim dec-row" data-row="r1" data-tone="danger"><td>a</td><td class="num"></td><td class="cell-shrink"></td></tr>',
    );
  });

  it("marks the current row with aria-current", () => {
    expect(tableRowHTML([{ label: "A" }], { cells: ["a"], current: true })).toBe(
      '<tr aria-current="true"><td>a</td></tr>',
    );
    expect(tableRowHTML([{ label: "A" }], { cells: ["a"], current: false })).toBe(
      "<tr><td>a</td></tr>",
    );
  });

  it("refuses more cells than columns", () => {
    expect(() => tableRowHTML([{ label: "A" }], { cells: [1, 2] })).toThrow(/2 cells for 1/);
  });

  it("writes the caret and, open, the detail row across the table", () => {
    const html = tableRowHTML(
      [{ label: "A" }],
      { cells: ["x"], id: "run1", expanded: true, detailHTML: "<p>steps</p>" },
      { expandable: true },
    );
    expect(html).toContain(
      '<td class="cell-caret"><button class="icon-btn icon-btn-xs icon-btn-ghost"',
    );
    expect(html).toContain('aria-expanded="true" data-expand="run1"');
    expect(html).toContain('aria-label="Details"');
    expect(
      html.endsWith(
        '<tr class="list-table-detail" data-detail="run1"><td colspan="2"><p>steps</p></td></tr>',
      ),
    ).toBe(true);
    const closed = tableRowHTML(
      [{ label: "A" }],
      { cells: ["x"], id: "run1", expanded: false, detailHTML: "<p>steps</p>" },
      { expandable: true },
    );
    expect(closed).toContain('aria-expanded="false"');
    expect(closed).not.toContain("list-table-detail");
  });

  it("refuses a detail outside an expandable table", () => {
    expect(() => tableRowHTML([{ label: "A" }], { cells: [], expanded: true })).toThrow(
      /only in an expandable table/,
    );
  });
});

describe("tableHTML", () => {
  it("wraps the table so it scrolls on its own", () => {
    expect(
      tableHTML([{ label: "A" }], [{ cells: [1] }], { id: "t", bodyId: "tb", label: "Pods" }),
    ).toBe(
      '<div class="table-wrap"><table id="t" class="list-table" aria-label="Pods">' +
        '<thead><tr><th scope="col">A</th></tr></thead><tbody id="tb"><tr><td>1</td></tr></tbody></table></div>',
    );
  });

  it("shows the empty or loading row across every column", () => {
    const empty = tableHTML(cols, [], { empty: "No pods", bare: true, expandable: true });
    expect(empty.startsWith("<table")).toBe(true);
    expect(empty).toContain('<td colspan="4">No pods</td>');
    expect(tableHTML(cols, [], { loading: true })).toContain('aria-busy="true"');
    expect(tableHTML(cols, [])).toContain("<tbody></tbody>");
    expect(tableSpan(cols)).toBe(3);
  });
});

describe("setTableSort", () => {
  it("moves aria-sort to the column asked and resets the rest", () => {
    const sorts: Record<string, string> = {};
    const th = (key: string) => ({
      dataset: { sort: key },
      setAttribute: (_: string, v: string) => {
        sorts[key] = v;
      },
    });
    const table = { querySelectorAll: () => [th("cpu"), th("mem")] } as unknown as HTMLElement;
    setTableSort(table, "mem", "ascending");
    expect(sorts).toEqual({ cpu: "none", mem: "ascending" });
  });
});
