import { describe, expect, it } from "vitest";
import {
  activeFilterCount,
  blockerEntry,
  blocksCounts,
  boardCards,
  boardIsTight,
  boardMatches,
  columnCollapsed,
  defaultView,
  groupChildren,
  isBlockedCard,
  liftTarget,
  narrowing,
  needsBlockerFetch,
  needsYou,
  neighborCard,
  orderChildren,
  parseView,
  pruneBlockers,
  viewKey,
  type BlockerCache,
} from "./board-view";

describe("remembered view", () => {
  it("keys the view by board", () => {
    expect(viewKey("shop")).toBe("octopus.board.view.shop");
  });

  it("reads junk and gaps as the default", () => {
    expect(parseView(null)).toEqual(defaultView());
    expect(parseView("not json")).toEqual(defaultView());
    expect(parseView("[1,2]").mode).toBe("nested");
    expect(parseView('{"mode":"weird","types":[1,"bug"],"collapsed":{"a":true,"b":"x"}}')).toEqual({
      ...defaultView(),
      types: ["bug"],
      collapsed: { a: true },
    });
  });

  it("round-trips what the page writes", () => {
    const v = {
      types: ["", "bug"],
      blocked: true,
      needsYou: true,
      mode: "cards" as const,
      expanded: ["p1"],
      collapsed: { done: false },
      listFolded: { done: false, review: true },
    };
    expect(parseView(JSON.stringify(v))).toEqual(v);
  });

  it("opens a view saved before the list existed with nothing folded by hand", () => {
    expect(parseView('{"mode":"cards"}').listFolded).toEqual({});
    expect(parseView('{"listFolded":["done"]}').listFolded).toEqual({});
  });

  it("counts the filters, not the subtask mode", () => {
    expect(activeFilterCount(defaultView())).toBe(0);
    expect(
      activeFilterCount({ ...defaultView(), types: ["a", "b"], needsYou: true, mode: "cards" }),
    ).toBe(2);
  });
});

describe("filters", () => {
  it("calls a card blocked only while an unmet hard dependency holds it in the first stage", () => {
    const t = { id: "a", stage: "backlog", is_blocked: true, blocked_by_count: 1 };
    expect(isBlockedCard(t, "backlog")).toBe(true);
    expect(isBlockedCard({ ...t, stage: "ready" }, "backlog")).toBe(false);
    expect(isBlockedCard({ ...t, blocked_by_count: 0 }, "backlog")).toBe(false);
    expect(isBlockedCard({ ...t, is_blocked: false }, "backlog")).toBe(false);
  });

  it("finds what waits on a person", () => {
    const t = { id: "a", stage: "x" };
    expect(needsYou({ ...t, status: "awaiting_input" })).toBe(true);
    expect(needsYou({ ...t, status: "failed" })).toBe(true);
    expect(needsYou({ ...t, status: "not_started" })).toBe(true);
    expect(needsYou({ ...t, status: "credential_blocked" })).toBe(true);
    expect(needsYou({ ...t, status: "resume_failed" })).toBe(true);
    expect(needsYou({ ...t, status: "stopped" })).toBe(false);
    expect(needsYou({ ...t, pending_approvals: 2 })).toBe(true);
    expect(needsYou({ ...t, status: "running", open_questions: 1 })).toBe(true);
    expect(needsYou({ ...t, status: "running" })).toBe(false);
    expect(needsYou({ ...t, status: "idle" })).toBe(false);
  });
});

describe("subtasks on the board", () => {
  const all = [
    { id: "p", created_at: "1" },
    { id: "c2", parent_id: "p", created_at: "3" },
    { id: "c1", parent_id: "p", created_at: "2" },
    { id: "c3", parent_id: "p", created_at: "4", sort_order: 1 },
    { id: "orphan", parent_id: "elsewhere", created_at: "5" },
    { id: "q", created_at: "6" },
  ];

  it("puts placed subtasks first, then the rest oldest first", () => {
    expect(orderChildren(all.filter((x) => x.parent_id === "p")).map((x) => x.id)).toEqual([
      "c3",
      "c1",
      "c2",
    ]);
  });

  it("groups children under their parent and keeps orphans on the board", () => {
    const g = groupChildren(all);
    expect(g.top.map((x) => x.id)).toEqual(["p", "orphan", "q"]);
    expect(g.children.get("p")?.map((x) => x.id)).toEqual(["c3", "c1", "c2"]);
    expect(g.children.has("q")).toBe(false);
  });

  it("draws subtasks as cards only in As cards mode", () => {
    const g = groupChildren(all);
    expect(boardCards(g, all, "nested")).toHaveLength(3);
    expect(boardCards(g, all, "cards")).toHaveLength(6);
  });
});

describe("filters in Nested mode", () => {
  const p = { id: "p", stage: "dev" };
  const lone = { id: "q", stage: "dev" };
  const kids = new Map([
    [
      "p",
      [
        { id: "a", stage: "backlog" },
        { id: "b", stage: "backlog" },
      ],
    ],
  ]);
  const hit = (id: string) => (x: { id: string }) => x.id === id;

  it("keeps a parent whose subtask matches and lists the subtask", () => {
    const m = boardMatches([p, lone], kids, "nested", hit("b"));
    expect([...m.cards]).toEqual(["p"]);
    expect([...(m.kids.get("p") ?? [])]).toEqual(["b"]);
  });

  it("drops a parent when neither it nor a subtask matches", () => {
    expect(boardMatches([p, lone], kids, "nested", hit("q")).cards).toEqual(new Set(["q"]));
  });

  it("looks at the card alone in As cards mode", () => {
    const m = boardMatches([p, lone], kids, "cards", hit("b"));
    expect(m.cards.size).toBe(0);
    expect(m.kids.size).toBe(0);
  });

  it("opens matching rows only for a filter or a search", () => {
    expect(narrowing(defaultView(), "")).toBe(false);
    expect(narrowing(defaultView(), "gift")).toBe(true);
    expect(narrowing({ ...defaultView(), blocked: true }, "")).toBe(true);
    expect(narrowing({ ...defaultView(), types: ["bug"] }, "")).toBe(false);
  });
});

describe("columns", () => {
  const base = { visible: 2, terminal: false, dragging: false, tight: false };

  it("lets a choice made by hand win", () => {
    expect(columnCollapsed({ ...base, manual: true })).toBe(true);
    expect(columnCollapsed({ ...base, visible: 0, manual: false })).toBe(false);
  });

  it("folds an empty column until a drag starts", () => {
    expect(columnCollapsed({ ...base, visible: 0 })).toBe(true);
    expect(columnCollapsed({ ...base, visible: 0, dragging: true })).toBe(false);
  });

  it("folds the last stage only while the board is too narrow", () => {
    expect(columnCollapsed({ ...base, terminal: true })).toBe(false);
    expect(columnCollapsed({ ...base, terminal: true, tight: true })).toBe(true);
    expect(columnCollapsed({ ...base, tight: true })).toBe(false);
  });

  it("measures whether every column fits", () => {
    expect(boardIsTight(1156, 6, 8)).toBe(true);
    expect(boardIsTight(1640, 6, 8)).toBe(false);
    expect(boardIsTight(0, 6, 8)).toBe(false);
  });
});

describe("blocker names", () => {
  const terminal = (s: string) => s === "done";

  it("fetches once per blocked count", () => {
    const cache: BlockerCache = new Map();
    expect(needsBlockerFetch(cache, { id: "a" })).toBe(false);
    expect(needsBlockerFetch(cache, { id: "a", blocked_by_count: 1 })).toBe(true);
    cache.set("a", blockerEntry(1, [], terminal));
    expect(needsBlockerFetch(cache, { id: "a", blocked_by_count: 1 })).toBe(false);
    expect(needsBlockerFetch(cache, { id: "a", blocked_by_count: 2 })).toBe(true);
  });

  it("keeps the unmet hard blockers only", () => {
    const e = blockerEntry(
      1,
      [
        { task_id: "x", title: "X", stage: "build", kind: "hard" },
        { task_id: "y", title: "Y", stage: "done", kind: "hard" },
        { task_id: "z", title: "Z", stage: "build", kind: "soft" },
      ],
      terminal,
    );
    expect(e.blockers.map((b) => b.task_id)).toEqual(["x"]);
  });

  it("counts what each card blocks from the entries still current", () => {
    const cache: BlockerCache = new Map([
      ["a", { count: 1, blockers: [{ task_id: "x", title: "X", stage: "s", kind: "hard" }] }],
      ["b", { count: 1, blockers: [{ task_id: "x", title: "X", stage: "s", kind: "hard" }] }],
      ["c", { count: 2, blockers: [{ task_id: "y", title: "Y", stage: "s", kind: "hard" }] }],
    ]);
    const counts = blocksCounts(cache, [
      { id: "a", blocked_by_count: 1 },
      { id: "b", blocked_by_count: 1 },
      { id: "c", blocked_by_count: 1 },
    ]);
    expect(counts.get("x")).toBe(2);
    expect(counts.has("y")).toBe(false);
  });

  it("forgets tasks that left the board", () => {
    const cache: BlockerCache = new Map([
      ["a", { count: 1, blockers: [] }],
      ["b", { count: 1, blockers: [] }],
    ]);
    pruneBlockers(cache, new Set(["b"]));
    expect([...cache.keys()]).toEqual(["b"]);
  });
});

describe("keyboard focus", () => {
  const cols = [["a1", "a2", "a3"], [], ["c1"], ["d1", "d2"]];

  it("moves up and down within a column", () => {
    expect(neighborCard(cols, "a2", "ArrowUp")).toBe("a1");
    expect(neighborCard(cols, "a2", "ArrowDown")).toBe("a3");
    expect(neighborCard(cols, "a3", "ArrowDown")).toBeNull();
    expect(neighborCard(cols, "a1", "ArrowUp")).toBeNull();
  });

  it("jumps over empty columns and lands on the same row or the last card", () => {
    expect(neighborCard(cols, "a3", "ArrowRight")).toBe("c1");
    expect(neighborCard(cols, "c1", "ArrowRight")).toBe("d1");
    expect(neighborCard(cols, "d2", "ArrowLeft")).toBe("c1");
    expect(neighborCard(cols, "a1", "ArrowLeft")).toBeNull();
    expect(neighborCard(cols, "nope", "ArrowLeft")).toBeNull();
  });

  it("walks a picked-up card along its own flow", () => {
    expect(liftTarget(["a", "b", "c"], "b", 1)).toBe("c");
    expect(liftTarget(["a", "b", "c"], "a", -1)).toBeNull();
    expect(liftTarget(["a", "c"], "b", 1)).toBeNull();
  });
});
