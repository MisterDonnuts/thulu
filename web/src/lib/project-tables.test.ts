import { describe, expect, it } from "vitest";
import {
  dimColumns,
  dimRowsHTML,
  topTaskColumns,
  topTaskRowsHTML,
  type DimensionRow,
  type TaskRow,
} from "./project-tables";

const dim = (o: Partial<DimensionRow> = {}): DimensionRow => ({
  key: "build",
  sessions: 3,
  tasks: 2,
  total_tokens: 12000,
  cost_usd: 1.5,
  cost_per_task: 0.75,
  ...o,
});

const task = (o: Partial<TaskRow> = {}): TaskRow => ({
  task_id: "t1",
  title: "Fix the cart",
  sessions: 2,
  turns: 9,
  total_tokens: 5000,
  cost_usd: 0.4,
  total_duration_ms: 90_000,
  ...o,
});

describe("dimColumns", () => {
  it("names the first column after the dimension and right-aligns the figures", () => {
    const cols = dimColumns("model");
    expect(cols[0]).toMatchObject({ label: "Model", mono: true });
    expect(cols.slice(1).every((c) => c.num)).toBe(true);
  });
});

describe("dimRowsHTML", () => {
  it("says there is no data in a row across every column", () => {
    const html = dimRowsHTML(dimColumns("stage"), []);
    expect(html).toContain('colspan="5"');
    expect(html).toContain('class="row-empty"');
  });
  it("labels each cell for the stacked phone layout", () => {
    const html = dimRowsHTML(dimColumns("stage"), [dim()]);
    expect(html).toContain('data-label="Stage"');
    expect(html).toContain('data-label="Cost"');
  });
  it("gives each row its share of the total cost as a meter", () => {
    const html = dimRowsHTML(dimColumns("stage"), [dim(), dim({ key: "review", cost_usd: 0.5 })]);
    expect(html.match(/dim-share/g)?.length).toBe(2);
    expect(html).toContain('aria-valuenow="75"');
    expect(html).toContain('aria-valuenow="25"');
  });
  it("shows a dash for an empty key and for cost per task with no tasks", () => {
    const html = dimRowsHTML(dimColumns("stage"), [dim({ key: "", tasks: 0 })]);
    expect(html.match(/>—</g)?.length).toBe(2);
  });
  it("escapes the key", () => {
    expect(dimRowsHTML(dimColumns("stage"), [dim({ key: "<img>" })])).not.toContain("<img>");
  });
});

describe("topTaskRowsHTML", () => {
  it("links the title to the task and puts its id under it", () => {
    const html = topTaskRowsHTML(topTaskColumns(), [task()]);
    expect(html).toContain('<a href="/tasks/#task-t1">Fix the cart</a>');
    expect(html).toContain('<span class="cell-sub">t1</span>');
  });
  it("shows a deleted task by its id alone", () => {
    const html = topTaskRowsHTML(topTaskColumns(), [task({ title: undefined })]);
    expect(html).toContain('<a class="mono" href="/tasks/#task-t1">t1</a>');
    expect(html).not.toContain("cell-sub");
  });
  it("spans the six columns when there are no tasks", () => {
    expect(topTaskRowsHTML(topTaskColumns(), [])).toContain('colspan="6"');
  });
});
