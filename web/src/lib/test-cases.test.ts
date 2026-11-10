import { afterEach, describe, expect, it, vi } from "vitest";
import {
  STATUS_ORDER,
  caseAnchor,
  casePriorityTagHTML,
  parseTaskHash,
  caseProgressHTML,
  priorityLabel,
  runAgoLabel,
  statusLabel,
  statusPillClasses,
  statusPillHTML,
  summarize,
  taskAnchor,
  type TestCasePriority,
  type TestCaseStatus,
} from "./test-cases";

describe("statusPillHTML", () => {
  it.each([
    ["pass", "badge badge-ok badge-pill"],
    ["fail", "badge badge-danger badge-pill"],
    ["blocked", "badge badge-warn badge-pill"],
    ["skipped", "badge badge-pill badge-hollow"],
    ["pending", "badge badge-muted badge-pill"],
  ] as const)("draws %s as a pill badge in its tone", (s, cls) => {
    expect(statusPillHTML(s)).toBe(`<span class="${cls}">${statusLabel(s)}</span>`);
  });

  it("covers every status in STATUS_ORDER", () => {
    for (const s of STATUS_ORDER) expect(statusPillHTML(s)).toMatch(/^<span class="badge /);
  });

  it("escapes a status the page does not know and paints it as pending", () => {
    const html = statusPillHTML('<img onerror="x">' as TestCaseStatus);
    expect(html).toContain('class="badge badge-muted badge-pill"');
    expect(html).not.toContain("<img");
  });
});

describe("statusPillClasses", () => {
  it("gives the picker the pill's classes plus its own", () => {
    expect(statusPillClasses("fail", "tc-status-select")).toBe(
      "badge badge-danger badge-pill tc-status-select",
    );
    expect(statusPillClasses("nope")).toBe("badge badge-muted badge-pill");
  });
});

describe("labels", () => {
  it("capitalises statuses and priorities", () => {
    expect(statusLabel("blocked")).toBe("Blocked");
    expect(priorityLabel("critical")).toBe("Critical");
  });
});

describe("summarize", () => {
  it("counts every status and the covered cases", () => {
    const s = summarize([
      { status: "pass", covered: true },
      { status: "pass", covered: true },
      { status: "fail", covered: true },
      { status: "pending", covered: false },
    ]);
    expect(s).toEqual({
      total: 4,
      covered: 3,
      byStatus: { pending: 1, pass: 2, fail: 1, blocked: 0, skipped: 0 },
    });
  });

  it("is all zeros for no cases", () => {
    expect(summarize([]).total).toBe(0);
    expect(summarize([]).covered).toBe(0);
  });
});

describe("caseProgressHTML", () => {
  it("draws one segment per status that has cases, in STATUS_ORDER", () => {
    const html = caseProgressHTML(
      summarize([
        { status: "pending", covered: false },
        { status: "pass", covered: true },
        { status: "pass", covered: true },
        { status: "fail", covered: true },
      ]),
    );
    expect(html).toMatch(/^<span class="seg-progress"/);
    expect(html.match(/class="seg-progress-seg"/g)).toHaveLength(3);
    expect(html.indexOf('data-tone="ok"')).toBeLessThan(html.indexOf('data-tone="faint"'));
    expect(html).toContain('data-tone="ok" style="flex-basis:50%" title="2 pass"');
    expect(html).toContain('data-tone="danger" style="flex-basis:25%" title="1 fail"');
    expect(html).toContain('aria-label="2 pass, 1 fail, 1 pending"');
  });

  it("rounds thirds to two decimals", () => {
    const html = caseProgressHTML(
      summarize([
        { status: "pass", covered: true },
        { status: "fail", covered: true },
        { status: "blocked", covered: true },
      ]),
    );
    expect(html).toContain("flex-basis:33.33%");
  });

  it("draws the empty track for no cases, named for what it lacks", () => {
    expect(caseProgressHTML(summarize([]))).toBe(
      '<span class="seg-progress" role="img" aria-label="no test cases"></span>',
    );
  });

  it("takes the caller's size and placement class", () => {
    expect(caseProgressHTML(summarize([]), { size: "xs", cls: "tc-head-bar" })).toContain(
      'class="seg-progress seg-progress-xs tc-head-bar"',
    );
  });
});

describe("caseAnchor / parseTaskHash", () => {
  it("round-trips a task and a case", () => {
    const href = caseAnchor("abc123", "TC-2", "b1");
    expect(href).toBe("/tasks/?board=b1#task-abc123/TC-2");
    expect(parseTaskHash(href.slice(href.indexOf("#")))).toEqual({
      taskId: "abc123",
      caseKey: "TC-2",
    });
  });

  it("carries the task's board, escaped, and leaves it out when unknown", () => {
    expect(taskAnchor("abc", "a b&c")).toBe("/tasks/?board=a%20b%26c#task-abc");
    expect(taskAnchor("abc", "")).toBe("/tasks/#task-abc");
  });

  it("reads a task-only hash with no case", () => {
    expect(parseTaskHash("#task-abc")).toEqual({ taskId: "abc", caseKey: null });
    expect(parseTaskHash("#task-abc/")).toEqual({ taskId: "abc", caseKey: null });
  });

  it.each(["", "#", "#tasks", "#task-", "#task-/TC-1", "task-abc"])("rejects %j", (h) => {
    expect(parseTaskHash(h)).toBeNull();
  });

  it("decodes an encoded key and survives a malformed one", () => {
    expect(parseTaskHash("#task-a/TC%2D3")?.caseKey).toBe("TC-3");
    expect(parseTaskHash("#task-a/TC-%")?.caseKey).toBe("TC-%");
  });
});

describe("runAgoLabel", () => {
  afterEach(() => vi.useRealTimers());

  it("reads in minutes, so it holds still between refreshes", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:05:00Z"));
    expect(runAgoLabel("2026-01-01T12:00:00Z")).toBe("5m ago");
    vi.setSystemTime(new Date("2026-01-01T12:05:20Z"));
    expect(runAgoLabel("2026-01-01T12:00:00Z")).toBe("5m ago");
    expect(runAgoLabel("2026-01-01T12:05:10Z")).toBe("just now");
    expect(runAgoLabel("")).toBe("");
  });
});

describe("casePriorityTagHTML", () => {
  it("puts a case priority on the task priority scale", () => {
    expect(casePriorityTagHTML("critical")).toMatch(/^<span class="tag tag-danger tag-inline">/);
    expect(casePriorityTagHTML("critical")).toContain('<span class="tag-label">Critical</span>');
    expect(casePriorityTagHTML("high")).toContain("#i-arrow-up");
    expect(casePriorityTagHTML("medium")).toMatch(/^<span class="tag tag-inline">/);
    expect(casePriorityTagHTML("low")).toMatch(/^<span class="tag tag-muted tag-inline">/);
  });

  it("is empty for a priority the scale does not know", () => {
    expect(casePriorityTagHTML("someday" as TestCasePriority)).toBe("");
  });

  it("takes other words and keeps the level's tone", () => {
    const html = casePriorityTagHTML("high", { label: 'Priority: <b>"High"</b>', size: "sm" });
    expect(html).toMatch(/^<span class="tag tag-sm tag-warn tag-inline">/);
    expect(html).toContain("Priority: &lt;b&gt;&quot;High&quot;&lt;/b&gt;");
  });
});
