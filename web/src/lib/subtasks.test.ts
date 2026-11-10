import { describe, expect, it } from "vitest";
import {
  dropInOrder,
  moveAmong,
  moveInOrder,
  parsePastedList,
  subtaskBarHTML,
  subtaskProgressHTML,
  subtaskSegments,
  startTarget,
  subtaskProgress,
  subtaskRowName,
  subtaskState,
  subtaskStateIconHTML,
  waitsLineHTML,
  type SubtaskLike,
} from "./subtasks";

const terminal = (s: string) => s === "done";
const st = (over: Partial<SubtaskLike>): SubtaskLike => ({
  id: "x",
  title: "x",
  stage: "backlog",
  ...over,
});

describe("subtaskState", () => {
  it("reads done from a terminal stage before anything else", () => {
    expect(subtaskState(st({ stage: "done", status: "failed" }), terminal, "backlog")).toBe("done");
  });

  it("reads the run first, then the blockers, then the stage", () => {
    expect(subtaskState(st({ status: "running" }), terminal, "backlog")).toBe("running");
    expect(subtaskState(st({ status: "queued" }), terminal, "backlog")).toBe("running");
    expect(subtaskState(st({ status: "awaiting_input" }), terminal, "backlog")).toBe("question");
    expect(subtaskState(st({ status: "out_of_turns" }), terminal, "backlog")).toBe("failed");
    expect(subtaskState(st({ status: "not_started" }), terminal, "backlog")).toBe("failed");
    expect(subtaskState(st({ status: "credential_blocked" }), terminal, "backlog")).toBe("failed");
    expect(subtaskState(st({ is_blocked: true }), terminal, "backlog")).toBe("blocked");
    expect(subtaskState(st({}), terminal, "backlog")).toBe("todo");
    expect(subtaskState(st({ stage: "dev" }), terminal, "backlog")).toBe("active");
  });

  it("draws an icon for each state and the pulse dot while running", () => {
    expect(subtaskStateIconHTML("done")).toContain("#i-check-circle");
    expect(subtaskStateIconHTML("blocked")).toContain("#i-lock");
    expect(subtaskStateIconHTML("running")).toContain('class="dot dot-info dot-pulse"');
  });

  it("tells started work from untouched work", () => {
    expect(subtaskStateIconHTML("todo")).toContain('#i-circle"');
    expect(subtaskStateIconHTML("active")).toContain("#i-circle-half");
  });

  it("names a row without repeating the stage", () => {
    expect(subtaskRowName("Field", "Started", "Review")).toBe("Field, Started, Review");
    expect(subtaskRowName("Field", "Done", "Done")).toBe("Field, Done");
  });
});

describe("subtaskProgress", () => {
  const kids = [
    st({ id: "a", stage: "done" }),
    st({ id: "b", stage: "dev", status: "running" }),
    st({ id: "c", is_blocked: true }),
    st({ id: "d" }),
    st({ id: "e", stage: "dev", status: "failed" }),
  ];

  it("counts done and the bar's four segments", () => {
    const p = subtaskProgress(kids, terminal, "backlog");
    expect(p).toEqual({ total: 5, done: 1, segments: { done: 1, active: 2, blocked: 1, todo: 1 } });
    expect(subtaskProgressHTML(p, { text: "long" })).toContain(">1 of 5 done</span>");
  });

  it("draws one segment per non-empty group, sized by count", () => {
    const html = subtaskBarHTML(subtaskProgress(kids, terminal, "backlog"));
    expect(html).toMatch(/^<span class="seg-progress seg-progress-split" aria-hidden="true">/);
    expect(html).toContain(
      'class="seg-progress-seg seg-progress-outline" data-tone="info" style="flex-basis:40%"',
    );
    expect(html).toContain('data-tone="warn"');
    const none = subtaskBarHTML(subtaskProgress([st({ stage: "done" })], terminal, "backlog"));
    expect(none).not.toContain('data-tone="track"');
    expect(subtaskBarHTML(subtaskProgress([], terminal, "backlog"))).toBe("");
  });

  it("writes the card's short line and nothing for no subtasks", () => {
    const html = subtaskProgressHTML(subtaskProgress(kids, terminal, "backlog"));
    expect(html).toMatch(/^<span class="progress-line"><span class="seg-progress seg-progress-xs/);
    expect(html).toContain('<span class="progress-line-text">1 of 5</span>');
    expect(subtaskProgressHTML(subtaskProgress([], terminal, "backlog"))).toBe("");
  });

  it("orders the segments done, under way, blocked, not started", () => {
    const segs = subtaskSegments(subtaskProgress(kids, terminal, "backlog"));
    expect(segs.map((g) => [g.tone, g.value, !!g.outline])).toEqual([
      ["ok", 1, false],
      ["info", 2, true],
      ["warn", 1, true],
      ["track", 1, false],
    ]);
  });
});

describe("parsePastedList", () => {
  it("makes one title per line, without bullets, boxes or numbers", () => {
    const text = "- [ ] Schema\n* Endpoint\n\n1. Front field\n2) Tests\n[x] Docs";
    expect(parsePastedList(text)).toEqual(["Schema", "Endpoint", "Front field", "Tests", "Docs"]);
  });

  it("treats one line as a title, not a list", () => {
    expect(parsePastedList("Just one")).toEqual([]);
    expect(parsePastedList("Only\n\n")).toEqual([]);
  });

  it("handles Windows line ends", () => {
    expect(parsePastedList("a\r\nb")).toEqual(["a", "b"]);
  });
});

describe("moveInOrder and dropInOrder", () => {
  const ids = ["a", "b", "c", "d"];

  it("swaps with a neighbour and stops at the ends", () => {
    expect(moveInOrder(ids, "b", -1)).toEqual(["b", "a", "c", "d"]);
    expect(moveInOrder(ids, "b", 1)).toEqual(["a", "c", "b", "d"]);
    expect(moveInOrder(ids, "a", -1)).toBeNull();
    expect(moveInOrder(ids, "d", 1)).toBeNull();
    expect(moveInOrder(ids, "z", 1)).toBeNull();
  });

  it("drops before the item now at an index", () => {
    expect(dropInOrder(ids, "a", 3)).toEqual(["b", "c", "a", "d"]);
    expect(dropInOrder(ids, "d", 0)).toEqual(["d", "a", "b", "c"]);
    expect(dropInOrder(ids, "b", 4)).toEqual(["a", "c", "d", "b"]);
  });

  it("steps past the visible neighbour when done rows are hidden", () => {
    expect(moveAmong(ids, ["a", "b", "d"], "d", -1)).toEqual(["a", "d", "b", "c"]);
    expect(moveAmong(ids, ["a", "b", "d"], "b", 1)).toEqual(["a", "c", "d", "b"]);
    expect(moveAmong(ids, ["a", "b", "d"], "a", -1)).toBeNull();
    expect(moveAmong(ids, ids, "b", 1)).toEqual(moveInOrder(ids, "b", 1));
  });

  it("is null when nothing moves", () => {
    expect(dropInOrder(ids, "b", 1)).toBeNull();
    expect(dropInOrder(ids, "b", 2)).toBeNull();
    expect(dropInOrder(ids, "z", 0)).toBeNull();
  });
});

describe("waitsLineHTML", () => {
  const w = (over: Partial<{ blockers: string[]; soft: string[]; ready: boolean }> = {}) => ({
    blockers: [],
    soft: [],
    ready: false,
    ...over,
  });

  it("names who a blocked subtask waits for", () => {
    expect(waitsLineHTML("blocked", w({ blockers: ["Schema"] }))).toContain("Waits for Schema");
    expect(waitsLineHTML("blocked", w({ blockers: ["Schema", "API", "UI"] }))).toContain(
      "Waits for Schema and 2 more",
    );
    expect(waitsLineHTML("blocked", w())).toContain("Waits for another task");
  });

  it("says Ready to start only once the blockers finished", () => {
    expect(waitsLineHTML("todo", w({ ready: true }))).toContain("Ready to start");
    expect(waitsLineHTML("todo", w())).toBe("");
    expect(waitsLineHTML("running", w({ ready: true }))).toBe("");
  });

  it("shows soft dependencies as reminders", () => {
    expect(waitsLineHTML("active", w({ soft: ["Field"] }))).toContain("After Field");
    expect(waitsLineHTML("active", w({ soft: ["Field", "API"] }))).toContain(
      "After Field and 1 more",
    );
    expect(waitsLineHTML("done", w({ soft: ["Field"] }))).toBe("");
  });

  it("escapes titles", () => {
    expect(waitsLineHTML("blocked", w({ blockers: ["<b>"] }))).toContain("&lt;b&gt;");
  });
});

describe("startTarget", () => {
  const flow = [
    { id: "backlog", label: "Backlog" },
    { id: "ready", label: "Ready" },
    { id: "dev", label: "In progress" },
    { id: "done", label: "Done", terminal: true },
  ];

  it("starts in the first later stage with an agent", () => {
    expect(startTarget(flow, "backlog", { dev: "frontend" })).toEqual({
      stage: "dev",
      label: "Start in In progress",
      runs: true,
    });
  });

  it("names a plain move when no agent works later", () => {
    expect(startTarget(flow, "backlog", {})).toEqual({
      stage: "ready",
      label: "Move to Ready",
      runs: false,
    });
    expect(startTarget(flow, "done", {})).toBeNull();
    expect(startTarget(flow, "nowhere", {})).toBeNull();
  });
});
