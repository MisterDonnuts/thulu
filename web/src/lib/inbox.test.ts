import { describe, expect, it } from "vitest";
import {
  approvalRowsHTML,
  inboxBadgeText,
  inboxColumns,
  inboxTotal,
  questionHeadline,
  questionRowsHTML,
  sortApprovals,
  sortQuestions,
  type InboxApproval,
  type InboxQuestion,
  stalledRowsHTML,
} from "./inbox";

const q = (over: Partial<InboxQuestion>): InboxQuestion => ({
  id: "q",
  comment_id: "c",
  kind: "question",
  status: "open",
  task_id: "t1",
  task_title: "Checkout",
  stage: "in_progress",
  payload: { questions: [{ question: "Which cache?" }] },
  ...over,
});

const a = (over: Partial<InboxApproval>): InboxApproval => ({
  id: "a",
  label: "Design",
  status: "pending",
  task_id: "t1",
  task_title: "Checkout",
  stage: "review",
  ...over,
});

describe("sortQuestions", () => {
  it("puts blockers first, then the longest waiting", () => {
    const out = sortQuestions([
      q({ id: "new", created_at: "2026-10-02T00:00:00Z" }),
      q({ id: "old", created_at: "2026-10-01T00:00:00Z" }),
      q({ id: "blk", kind: "blocker", created_at: "2026-10-03T00:00:00Z" }),
    ]);
    expect(out.map((x) => x.id)).toEqual(["blk", "old", "new"]);
  });
});

describe("sortApprovals", () => {
  it("puts requested changes first", () => {
    const out = sortApprovals([
      a({ id: "p", created_at: "2026-10-01T00:00:00Z" }),
      a({ id: "c", status: "changes_requested", created_at: "2026-10-02T00:00:00Z" }),
    ]);
    expect(out.map((x) => x.id)).toEqual(["c", "p"]);
  });
});

describe("questionHeadline", () => {
  it("shows the first question and counts the rest", () => {
    expect(questionHeadline(q({}))).toBe("Which cache?");
    const two = q({ payload: { questions: [{ question: "A?" }, { question: "B?" }] } });
    expect(questionHeadline(two)).toBe("A? (+1 more)");
  });

  it("falls back to the context", () => {
    expect(questionHeadline(q({ payload: { context: "stuck", questions: [] } }))).toBe("stuck");
  });
});

describe("rows", () => {
  const labels = new Map([
    ["in_progress", "In progress"],
    ["review", "Review"],
  ]);

  it("links each question to its task drawer, on its board", () => {
    const html = questionRowsHTML([q({ board_id: "bugfix", agent: "dev" })], labels);
    expect(html).toContain('href="/tasks/?board=bugfix#task-t1"');
    expect(html).toContain("In progress");
    expect(html).toContain("Which cache?");
    expect(html).toContain('<span class="badge badge-accent badge-sm inbox-kind">');
    expect(html).toContain('<span class="tag tag-sm tag-special tag-inline">');
    expect(html).toContain('<span class="tag-label">In progress</span>');
  });

  it("marks a blocker", () => {
    expect(questionRowsHTML([q({ kind: "blocker" })], labels)).toContain('<tr data-tone="warn">');
    expect(questionRowsHTML([q({})], labels)).not.toContain("data-tone");
  });

  it("says a question that does not block leaves the agent working", () => {
    expect(questionRowsHTML([q({ blocking: false })], labels)).toContain("Question, still working");
    expect(questionRowsHTML([q({})], labels)).not.toContain("still working");
  });

  it("shows an approval's label, reason and stage, escaped", () => {
    const html = approvalRowsHTML([a({ label: "<b>QA</b>", reason: "touches UI" })], labels);
    expect(html).toContain("&lt;b&gt;QA");
    expect(html).toContain("touches UI");
    expect(html).toContain("Review");
  });

  it("opens an approval on the Approvals tab, and offers Approve in the row", () => {
    const html = approvalRowsHTML([a({ id: "ap1", board_id: "bugfix" })], labels);
    expect(html).toContain('href="/tasks/?board=bugfix#task-t1/approvals"');
    expect(html).toContain('data-approve="ap1" data-task="t1"');
  });

  it("falls back to the task's stage for an approval with none", () => {
    const html = approvalRowsHTML([a({ stage: "", task_stage: "review" })], labels);
    expect(html).toContain("Review");
  });

  it("marks requested changes and names every cell for the phone's stacked rows", () => {
    const html = approvalRowsHTML([a({ status: "changes_requested" })], labels);
    expect(html).toMatch(/^<tr data-tone="warn">/);
    expect(html).toContain('<td class="cell-title" data-label="Task">');
    expect(html).toContain('<td class="cell-shrink inbox-when" data-label="Waiting">');
    expect(approvalRowsHTML([a({})], labels)).not.toContain("data-tone");
  });
});

describe("inboxColumns", () => {
  it("gives the task a fixed share and leaves the rest to What, so a title keeps its line", () => {
    const cols = inboxColumns();
    expect(cols.map((c) => c.width)).toEqual(["title", undefined, "shrink", "shrink"]);
  });

  it("writes the same widths on the cells of every table", () => {
    const labels = new Map([["review", "Review"]]);
    for (const html of [
      questionRowsHTML([q({})], labels),
      approvalRowsHTML([a({})], labels),
      stalledRowsHTML(
        [
          {
            task_id: "t1",
            task_title: "Checkout",
            task_stage: "review",
            status: "failed",
            updated_at: "2026-10-01T00:00:00Z",
          },
        ],
        labels,
      ),
    ]) {
      expect(html).toContain('<td class="cell-title" data-label="Task">');
      expect(html).toContain('<td data-label="What">');
      expect(html).not.toContain("cell-fill");
    }
  });
});

describe("stalledRowsHTML", () => {
  it("lists the tasks that could not resume, oldest first, with what to do", () => {
    const html = stalledRowsHTML(
      [
        {
          task_id: "b",
          task_title: "Newer",
          task_stage: "dev",
          status: "resume_failed",
          updated_at: "2026-10-02T00:00:00Z",
        },
        {
          task_id: "a",
          task_title: "Older",
          task_stage: "dev",
          status: "resume_failed",
          updated_at: "2026-10-01T00:00:00Z",
        },
      ],
      new Map([["dev", "Development"]]),
    );
    expect(html.indexOf("Older")).toBeLessThan(html.indexOf("Newer"));
    expect(html).toContain("Could not resume");
    expect(html).toMatch(/class="badge badge-\w+ badge-pill inbox-kind"/);
    expect(html).toContain("Relaunch stage");
    expect(html).toContain("Development");
  });

  it("names each status the way the task does, with what to do about it", () => {
    const row = (status: string) =>
      stalledRowsHTML(
        [
          {
            task_id: "a",
            task_title: "T",
            task_stage: "dev",
            status,
            updated_at: "2026-10-01T00:00:00Z",
          },
        ],
        new Map(),
      );
    expect(row("not_started")).toContain("Did not start");
    expect(row("not_started")).toContain("Retry");
    expect(row("no_agent")).toContain("Agent field");
    expect(row("credential_blocked")).toContain("Credentials");
    expect(row("blocked")).toContain("Review and retry");
    expect(row("out_of_turns")).toContain("More turns");
  });
});

describe("inboxTotal", () => {
  it("counts the tasks waiting, like the board's Needs you, and caps the badge", () => {
    expect(inboxTotal({ questions: [q({}), q({ id: "q2" })], approvals: [a({})] })).toBe(1);
    expect(
      inboxTotal({
        questions: [q({}), q({ id: "q2", task_id: "t2" })],
        approvals: [a({}), a({ id: "b", task_id: "t3" })],
      }),
    ).toBe(3);
    expect(inboxTotal({ questions: null })).toBe(0);
    expect(
      inboxTotal({
        questions: [q({})],
        stalled: [{ task_id: "s", status: "resume_failed", updated_at: "" }],
      }),
    ).toBe(2);
    expect(inboxBadgeText(7)).toBe("7");
    expect(inboxBadgeText(120)).toBe("99+");
  });
});
