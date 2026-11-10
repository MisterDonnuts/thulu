import { afterEach, describe, expect, it, vi } from "vitest";
import {
  agoLabel,
  blockedLabel,
  cardFootHTML,
  cardMetaHTML,
  cardPRs,
  cardProgressHTML,
  cardStateHTML,
  cardStateRow,
  kidsWord,
  nestedRowsHTML,
  partOfHTML,
  priorityIconHTML,
  shortId,
  showsNextAgent,
} from "./task-card";

afterEach(() => {
  vi.useRealTimers();
});

describe("agoLabel", () => {
  const at = (secondsAgo: number) => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:00:00Z"));
    return agoLabel(new Date(Date.now() - secondsAgo * 1000).toISOString());
  };

  it("says nothing for a missing or unparseable timestamp", () => {
    expect(agoLabel(undefined)).toBe("");
    expect(agoLabel("not a date")).toBe("");
  });

  it("rounds the first minute into words", () => {
    expect(at(5)).toBe("just now");
    expect(at(50)).toBe("<1m");
  });

  it("steps through minutes, hours and days", () => {
    expect(at(60 * 5)).toBe("5m");
    expect(at(60 * 60 * 3)).toBe("3h");
    expect(at(60 * 60 * 24 * 4)).toBe("4d");
  });

  it("never reads as being in the future", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T12:00:00Z"));
    expect(agoLabel("2026-01-01T12:05:00Z")).toBe("just now");
  });
});

describe("shortId", () => {
  it("takes the first six characters", () => {
    expect(shortId("a1b2c3d4e5")).toBe("a1b2c3");
    expect(shortId("abc")).toBe("abc");
  });
});

describe("priorityIconHTML", () => {
  it("draws urgent and high with a name, and nothing for the rest", () => {
    expect(priorityIconHTML("urgent")).toContain('aria-label="Urgent priority"');
    expect(priorityIconHTML("urgent")).toContain("#i-alert");
    expect(priorityIconHTML("high")).toContain("#i-arrow-up");
    expect(priorityIconHTML("medium")).toBe("");
    expect(priorityIconHTML(undefined)).toBe("");
  });
});

describe("state row", () => {
  it("skips idle and terminal cards", () => {
    expect(cardStateRow({ status: "idle" }, { terminal: false })).toBeNull();
    expect(cardStateRow({ status: "running" }, { terminal: true })).toBeNull();
    expect(cardStateRow({ status: "running" }, { terminal: true, agent: "dev" })).toBeNull();
  });

  it("names the stage's agent on an idle card", () => {
    const row = cardStateRow({ status: "idle" }, { terminal: false, agent: "frontend" });
    expect(row?.word).toBeNull();
    expect(row?.agent).toBe("frontend");
    expect(cardStateHTML(row)).toContain(">frontend<");
    expect(cardStateHTML(row)).not.toContain("badge");
    expect(cardStateHTML(row)).toContain('class="tag tag-special tag-inline"');
  });

  it("says the run state in words with the stage's agent", () => {
    const row = cardStateRow({ status: "running" }, { terminal: false, agent: "frontend" });
    expect(row?.word?.label).toBe("Running");
    const html = cardStateHTML(row);
    expect(html).toContain("dot-pulse");
    expect(html).toContain(">Running<");
    expect(html).toContain(">frontend<");
    expect(cardStateHTML(cardStateRow({ status: "failed" }, { terminal: false }))).toContain(
      'class="badge badge-danger badge-pill" data-run="failed"',
    );
  });

  it("says why a run did not start or has no agent on its own line", () => {
    const row = cardStateRow(
      {
        status: "no_agent",
        status_reason: "**Verify** has no agent. Pick one in the task's Agent field.",
      },
      { terminal: false },
    );
    expect(row?.word?.label).toBe("No agent");
    const html = cardStateHTML(row);
    expect(html).toContain(
      '<span class="task-card-reason" title="Verify has no agent. Pick one in the task&#39;s Agent field.">Verify has no agent. Pick one in the task&#39;s Agent field.</span>',
    );
    expect(cardStateHTML(cardStateRow({ status: "running" }, { terminal: false }))).not.toContain(
      "task-card-reason",
    );
    expect(cardStateRow({ status: "not_started" }, { terminal: false })?.word?.label).toBe(
      "Did not start",
    );
  });

  it("lets an idle card name its next column even with its own agent", () => {
    expect(showsNextAgent(null)).toBe(true);
    expect(
      showsNextAgent(cardStateRow({ status: "idle" }, { terminal: false, agent: "dev" })),
    ).toBe(true);
    expect(showsNextAgent(cardStateRow({ status: "running" }, { terminal: false }))).toBe(false);
  });

  it("reads a pending approval as Needs approval when the run says nothing", () => {
    expect(cardStateRow({ pending_approvals: 1 }, { terminal: false })?.word?.label).toBe(
      "Needs approval",
    );
    expect(
      cardStateRow({ status: "awaiting_input", pending_approvals: 1 }, { terminal: false })?.word
        ?.label,
    ).toBe("Needs an answer");
  });

  it("marks a question the run kept working through", () => {
    const row = cardStateRow({ status: "running", open_questions: 1 }, { terminal: false });
    expect(row?.asked).toBe(true);
    expect(row?.word?.label).toBe("Running");
    expect(cardStateHTML(row)).toContain('data-run="asked"');
    expect(
      cardStateRow({ status: "awaiting_input", open_questions: 1 }, { terminal: false })?.asked,
    ).toBe(false);
    expect(cardStateRow({ status: "idle", open_questions: 1 }, { terminal: false })?.asked).toBe(
      true,
    );
  });

  it("rolls a parent's subtasks that need someone up to the card", () => {
    const none = { question: 0, failed: 0, blocked: 0 };
    expect(kidsWord(none)).toBeNull();
    expect(kidsWord({ ...none, blocked: 2 })?.label).toBe("2 subtasks blocked");
    expect(kidsWord({ question: 1, failed: 1, blocked: 2 })?.label).toBe("1 subtask question");
    expect(kidsWord({ question: 1, failed: 1, blocked: 2 })?.title).toBe(
      "1 subtask needs an answer",
    );
    const row = cardStateRow({ status: "idle" }, { terminal: false, kids: { ...none, failed: 1 } });
    expect(row?.word).toBeNull();
    const html = cardStateHTML(row);
    expect(html).toContain(
      'class="badge badge-danger badge-pill" title="1 subtask failed" data-kids="failed"',
    );
    expect(html).toContain("1 subtask failed");
    expect(cardStateRow({}, { terminal: true, kids: { ...none, question: 1 } })?.kids?.id).toBe(
      "question",
    );
  });
});

describe("meta row", () => {
  const empty = { repos: [], tags: [], blocked: "", blocks: 0 };

  it("is empty for a bare task", () => {
    expect(cardMetaHTML(empty)).toBe("");
  });

  it("draws neutral repo chips with a coloured dot", () => {
    const html = cardMetaHTML({ ...empty, repos: ["a<b>"] });
    expect(html).toMatch(
      /^<span class="tag tag-sm" title="a&lt;b&gt;"><span class="tag-dot tag-dot-\w+"/,
    );
    expect(html).toContain("a&lt;b&gt;");
    expect(html).not.toMatch(/tag-sm tag-\w+" title="a/);
  });

  it("shows two tags, then +n with the rest in the title", () => {
    const html = cardMetaHTML({ ...empty, tags: ["a", "b", "c", "d"] });
    expect(html.match(/class="tag tag-sm tag-\w+"/g)).toHaveLength(2);
    expect(html).toContain(
      '<span class="tag tag-sm" title="c, d"><span class="tag-label">+2</span></span>',
    );
  });

  it("writes the blocked line and Blocks n", () => {
    const html = cardMetaHTML({ ...empty, blocked: "Blocked by Cart field", blocks: 1 });
    expect(html).toContain("#i-lock");
    expect(html).toContain('<span class="tag tag-sm tag-warn" title="Blocked by Cart field">');
    expect(html).toContain(">Blocked by Cart field<");
    expect(html).toContain(">Blocks 1<");
  });

  it("names the blocker when there is one, else counts", () => {
    expect(blockedLabel(1, ["Cart field"])).toBe("Blocked by Cart field");
    expect(blockedLabel(1, [])).toBe("Blocked by 1 task");
    expect(blockedLabel(2, ["Cart field"])).toBe("Blocked by 2 tasks");
    expect(blockedLabel(0, [])).toBe("");
  });
});

describe("foot", () => {
  it("numbers pull requests and drops unsafe links", () => {
    const prs = cardPRs({
      b: "https://github.com/o/r/pull/42",
      a: "javascript:alert(1)",
      c: "https://github.com/o/r2/pull/7",
    });
    expect(prs.map((p) => p.number)).toEqual(["42", "7"]);
  });

  it("writes PR, short id and age", () => {
    const html = cardFootHTML({
      id: "1e8e03aa",
      prs: [{ number: "42", url: "https://github.com/o/r/pull/42" }],
      age: "2d",
    });
    expect(html).toContain(">#42<");
    expect(html).toContain('title="PR #42"');
    expect(html).toContain(">#1e8e03<");
    expect(html).toContain(">2d<");
  });

  it("draws the progress only for a parent", () => {
    expect(cardProgressHTML(null)).toBe("");
    const html = cardProgressHTML({
      total: 4,
      done: 1,
      segments: { done: 1, active: 1, blocked: 1, todo: 1 },
    });
    expect(html).toContain("seg-progress seg-progress-xs seg-progress-split");
    expect(html).toContain(">1 of 4<");
  });
});

describe("subtask rows and Part of", () => {
  it("draws one button per subtask with a stage dot and a full name", () => {
    const html = nestedRowsHTML([
      {
        id: "s1",
        title: "Gift card field",
        state: "running",
        iconHTML: "<i></i>",
        stateLabel: "Running",
        stageLabel: "In progress",
        stageColor: "#f2994a",
      },
      {
        id: "s2",
        title: "E2E",
        state: "blocked",
        iconHTML: "",
        stateLabel: "Blocked",
        stageLabel: "Backlog",
        stageColor: "bad;",
      },
    ]);
    expect(html).toContain(
      '<li><button class="row row-sm row-wrap task-card-kid st-running" type="button" data-open-sub="s1"',
    );
    expect(html).toContain('<span class="row-title">Gift card field</span>');
    expect(html).toContain('aria-label="Gift card field, Running, In progress"');
    expect(html).toContain('style="--dot-color:#f2994a"');
    expect(html).not.toContain("stage-pill");
    expect(html).not.toContain("bad;");
  });

  it("says whose subtask a card is", () => {
    expect(partOfHTML("Pay <now>")).toContain("Part of Pay &lt;now&gt;");
    expect(partOfHTML("x")).toContain("#i-corner-up");
  });
});
