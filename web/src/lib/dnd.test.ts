import { describe, expect, it } from "vitest";
import { autoScrollDelta, blockedMoveMessage, nextStageInFlow } from "./dnd";
import type { Stage } from "./stages";

const stage = (id: string): Stage => ({
  id,
  label: id,
  color: "#fff",
  position: 0,
  auto_advance: false,
  blocked_by_flags: false,
  initial: false,
  terminal: false,
  ingest_flags: false,
  push_branch: false,
  open_pr: false,
  wip_group: "",
  virtual: false,
});

describe("autoScrollDelta", () => {
  const bounds = { left: 0, right: 1000 };

  it("does nothing in the middle of the board", () => {
    expect(autoScrollDelta(500, bounds)).toBe(0);
  });

  it("scrolls left near the left edge and right near the right one", () => {
    expect(autoScrollDelta(20, bounds)).toBeLessThan(0);
    expect(autoScrollDelta(980, bounds)).toBeGreaterThan(0);
  });

  it("honours a custom margin and step", () => {
    expect(autoScrollDelta(20, bounds, 10, 5)).toBe(0);
    expect(autoScrollDelta(5, bounds, 10, 5)).toBe(-5);
  });
});

describe("nextStageInFlow", () => {
  const flow = [stage("backlog"), stage("build"), stage("done")];

  it("steps forward and back", () => {
    expect(nextStageInFlow(flow, "build", 1)).toBe("done");
    expect(nextStageInFlow(flow, "build", -1)).toBe("backlog");
  });

  it("stops at both ends", () => {
    expect(nextStageInFlow(flow, "backlog", -1)).toBeNull();
    expect(nextStageInFlow(flow, "done", 1)).toBeNull();
  });

  it("steps over a column the flow skips", () => {
    const skipped = [stage("backlog"), stage("done")];
    expect(nextStageInFlow(skipped, "backlog", 1)).toBe("done");
  });

  it("refuses to move a task sitting outside its own flow", () => {
    expect(nextStageInFlow(flow, "review", 1)).toBeNull();
  });
});

describe("blockedMoveMessage", () => {
  it("falls back to the server's own error", () => {
    expect(blockedMoveMessage({ error: "stage is closed" })).toBe("stage is closed");
    expect(blockedMoveMessage({})).toBe("transition blocked");
  });

  it("names the tasks that are gating this one", () => {
    expect(blockedMoveMessage({ blocked_by: [{ title: "Build it" }] })).toBe(
      'Waiting on "Build it"',
    );
  });

  it("lists three blockers and counts the rest", () => {
    const deps = [1, 2, 3, 4, 5].map((n) => ({ title: `t${n}` }));
    const msg = blockedMoveMessage({ blocked_by: deps });
    expect(msg).toBe('Waiting on "t1", "t2", "t3" +2 more');
  });

  it("names the approvals still open", () => {
    expect(
      blockedMoveMessage({
        blocking_approvals: [
          { key: "design", label: "Design", status: "pending" },
          { key: "qa", label: "QA", status: "changes_requested" },
        ],
      }),
    ).toBe("Waiting on approval: Design, QA");
  });

  it("reports pending flags from an older daemon", () => {
    expect(
      blockedMoveMessage({ blocking_flags: [{ kind: "review", label: "needs a human" }] }),
    ).toBe("review: needs a human");
  });

  it("prefers dependencies over flags when both arrive", () => {
    const msg = blockedMoveMessage({
      blocked_by: [{ title: "Build it" }],
      blocking_flags: [{ kind: "review", label: "x" }],
    });
    expect(msg).toContain("Waiting on");
  });
});
