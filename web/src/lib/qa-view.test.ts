import { afterEach, describe, expect, it, vi } from "vitest";

import {
  EMPTY_FILTERS,
  caseRowHTML,
  countsSummary,
  matchedCovered,
  filtersFromQuery,
  filtersToQuery,
  groupByTask,
  groupHeadHTML,
  lastRunHTML,
  matchedTotal,
  tableBodyHTML,
  testCasesURL,
  toggleStatus,
  type QARow,
} from "./qa-view";

function row(over: Partial<QARow> = {}): QARow {
  return {
    id: "c1",
    task_id: "t1",
    key: "TC-1",
    title: "Login works",
    description: "",
    steps: "",
    expected: "",
    priority: "medium",
    status: "pending",
    covered: false,
    position: 1,
    created_by: "",
    updated_by: "",
    created_at: "2026-09-30T10:00:00Z",
    updated_at: "2026-09-30T10:00:00Z",
    task_title: "Auth",
    task_stage: "review",
    board_id: "b1",
    last_run: null,
    runs_count: 0,
    ...over,
  };
}

describe("filtersFromQuery", () => {
  it("reads every filter", () => {
    expect(
      filtersFromQuery("?status=fail,blocked&board_id=b1&project_id=p1&q=login&uncovered=1"),
    ).toEqual({
      status: ["fail", "blocked"],
      boardId: "b1",
      projectId: "p1",
      q: "login",
      uncovered: true,
    });
  });

  it("drops unknown statuses and keeps the canonical order", () => {
    expect(filtersFromQuery("?status=skipped,bogus,pass").status).toEqual(["pass", "skipped"]);
  });

  it("gives the empty filter for an empty query", () => {
    expect(filtersFromQuery("")).toEqual(EMPTY_FILTERS);
  });

  it("treats only uncovered=1 as on", () => {
    expect(filtersFromQuery("?uncovered=true").uncovered).toBe(false);
  });
});

describe("filtersToQuery", () => {
  it("is empty when nothing is set", () => {
    expect(filtersToQuery(EMPTY_FILTERS)).toBe("");
  });

  it("round-trips through filtersFromQuery", () => {
    const f = {
      status: ["pass" as const, "fail" as const],
      boardId: "b 1",
      projectId: "p&1",
      q: "log in",
      uncovered: true,
    };
    expect(filtersFromQuery(filtersToQuery(f))).toEqual({ ...f, status: ["pass", "fail"] });
  });

  it("trims the search text and skips a blank one", () => {
    expect(filtersToQuery({ ...EMPTY_FILTERS, q: "   " })).toBe("");
    expect(filtersToQuery({ ...EMPTY_FILTERS, q: " x " })).toBe("?q=x");
  });
});

describe("testCasesURL", () => {
  it("uses the API's parameter names", () => {
    expect(
      testCasesURL({ ...EMPTY_FILTERS, status: ["fail"], boardId: "b1", uncovered: true }),
    ).toBe("/test-cases?status=fail&board_id=b1&uncovered=1");
    expect(testCasesURL(EMPTY_FILTERS)).toBe("/test-cases");
  });
});

describe("toggleStatus", () => {
  it("adds a missing status and removes a present one", () => {
    const on = toggleStatus(EMPTY_FILTERS, "fail");
    expect(on.status).toEqual(["fail"]);
    expect(toggleStatus(on, "fail").status).toEqual([]);
  });

  it("keeps the canonical order", () => {
    const f = toggleStatus(toggleStatus(EMPTY_FILTERS, "pending"), "skipped");
    expect(f.status).toEqual(["skipped", "pending"]);
  });
});

describe("groupByTask", () => {
  it("keeps the API order of tasks and of cases", () => {
    const groups = groupByTask([
      row({ id: "a", task_id: "t2", task_title: "Two" }),
      row({ id: "b", task_id: "t1" }),
      row({ id: "c", task_id: "t2", task_title: "Two" }),
    ]);
    expect(groups.map((g) => g.taskId)).toEqual(["t2", "t1"]);
    expect(groups[0].rows.map((r) => r.id)).toEqual(["a", "c"]);
    expect(groups[0].title).toBe("Two");
  });
});

describe("countsSummary", () => {
  it("fills missing statuses with zero and totals them", () => {
    const s = countsSummary({ pass: 3, fail: 1 }, { pass: 3 });
    expect(s.total).toBe(4);
    expect(s.byStatus.blocked).toBe(0);
    expect(s.covered).toBe(3);
  });

  it("counts covered cases over the whole filtered set, not the page", () => {
    const covered = { pass: 300, fail: 20, pending: 0 };
    expect(matchedCovered(covered, [])).toBe(320);
    expect(matchedCovered(covered, ["fail", "pending"])).toBe(20);
  });

  it("totals only the picked statuses, or every status when none is picked", () => {
    const s = countsSummary({ pass: 3, fail: 1, pending: 2 });
    expect(matchedTotal(s, [])).toBe(6);
    expect(matchedTotal(s, ["fail", "pending"])).toBe(3);
  });
});

describe("HTML builders", () => {
  afterEach(() => vi.useRealTimers());

  it("escapes the case fields", () => {
    const html = caseRowHTML(row({ key: "TC-1", title: "<img src=x onerror=1>" }));
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
  });

  it("links the title and the Open cell to the case in its task, on its board", () => {
    const html = caseRowHTML(row({ task_id: "abc", key: "TC-2", board_id: "bugs" }));
    expect(html.split('href="/tasks/?board=bugs#task-abc/TC-2"')).toHaveLength(3);
  });

  it("links a case already in the Evidence library, and offers to add the rest", () => {
    const lib = { keys: new Map([["t1/TC-1", "EV-7"]]), canAdd: true };
    const inLib = caseRowHTML(row(), lib);
    expect(inLib).toContain('href="#case-EV-7"');
    expect(inLib).toContain("In library · EV-7");
    const other = caseRowHTML(row({ key: "TC-2" }), lib);
    expect(other).toContain('data-act="to-library" data-task="t1" data-key="TC-2"');
    expect(caseRowHTML(row({ key: "TC-2" }), { ...lib, canAdd: false })).not.toContain(
      "to-library",
    );
    expect(caseRowHTML(row())).not.toContain("to-library");
  });

  it("marks covered and uncovered cases", () => {
    expect(caseRowHTML(row({ covered: true }))).toContain('aria-label="Covered"');
    expect(caseRowHTML(row())).toContain('aria-label="Not covered"');
  });

  it("describes the last run or its absence", () => {
    expect(lastRunHTML(null)).toContain("Never run");
    const html = lastRunHTML({
      id: "r1",
      test_case_id: "c1",
      task_id: "t1",
      verdict: "fail",
      observed: "",
      note: "",
      video_attachment_id: "",
      take_attachment_id: "",
      report_id: "",
      comment_id: "",
      capture_attachment_ids: [],
      author: "<b>bot</b>",
      ran_at: new Date().toISOString(),
    });
    expect(html).toContain(
      '<span class="qa-verdict"><span class="dot dot-danger" aria-hidden="true"></span>Fail</span>',
    );
    expect(html).toContain("just now");
    expect(html).toContain("&lt;b&gt;bot");
  });

  it("names the viewer's own run You and never shows the dev-mode user", () => {
    const run = (author: string) => ({
      id: "r1",
      test_case_id: "c1",
      task_id: "t1",
      verdict: "pass" as const,
      observed: "",
      note: "",
      video_attachment_id: "",
      take_attachment_id: "",
      report_id: "",
      comment_id: "",
      capture_attachment_ids: [],
      author,
      ran_at: new Date().toISOString(),
    });
    expect(lastRunHTML(run("user"), { name: "user" })).toContain(" · You");
    expect(lastRunHTML(run("user"))).toContain(" · Someone");
    expect(lastRunHTML(run("qa"), { name: "user" })).toContain(" · qa");
    const html = tableBodyHTML(
      [
        {
          taskId: "t1",
          boardId: "b",
          title: "T",
          stage: "s",
          rows: [row({ last_run: run("user") })],
        },
      ],
      new Map(),
      undefined,
      { name: "user" },
    );
    expect(html).toContain(" · You");
  });

  it("keeps a row's HTML identical between refreshes within a minute", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:05:00Z"));
    const r = row({
      last_run: {
        id: "r1",
        test_case_id: "c1",
        task_id: "t1",
        verdict: "pass",
        observed: "",
        note: "",
        video_attachment_id: "",
        take_attachment_id: "",
        report_id: "",
        comment_id: "",
        capture_attachment_ids: [],
        author: "",
        ran_at: "2026-01-01T12:00:00Z",
      },
    });
    const first = caseRowHTML(r);
    vi.setSystemTime(new Date("2026-01-01T12:05:25Z"));
    expect(caseRowHTML(r)).toBe(first);
    expect(first).toContain("5m ago");
  });

  it("titles a group with its task and escapes it", () => {
    const [g] = groupByTask([row({ task_title: "A & B", status: "pass" }), row({ id: "c2" })]);
    const html = groupHeadHTML(g, "Review");
    expect(html).toContain('href="/tasks/?board=b1#task-t1"');
    expect(html).toContain("A &amp; B");
    expect(html).toContain("1/2 pass");
    expect(html).toMatch(/^<tr class="group-head-row" data-task="t1"><th colspan="8">/);
  });

  it("draws a head row per task followed by its cases", () => {
    const groups = groupByTask([row({ id: "a" }), row({ id: "b", task_id: "t2" })]);
    const html = tableBodyHTML(groups, new Map([["review", "In review"]]));
    expect(html.match(/class="group-head-row"/g)).toHaveLength(2);
    expect(html.match(/class="qa-row"/g)).toHaveLength(2);
    expect(html).toContain('data-label="Last run"');
    expect(html).toContain("In review");
  });
});
