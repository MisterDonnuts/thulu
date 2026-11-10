import { describe, expect, it } from "vitest";

import {
  FOLDED_REPLIES,
  groupThreads,
  inflightMentions,
  lastAgent,
  visibleReplies,
} from "./threads";

describe("groupThreads", () => {
  it("puts each reply under its root, roots in order", () => {
    const threads = groupThreads([
      { id: "a" },
      { id: "b" },
      { id: "a1", parent_id: "a" },
      { id: "b1", parent_id: "b" },
      { id: "a2", parent_id: "a" },
    ]);
    expect(threads.map((t) => [t.root.id, ...t.replies.map((r) => r.id)])).toEqual([
      ["a", "a1", "a2"],
      ["b", "b1"],
    ]);
  });

  it("keeps a reply whose root is not in the list", () => {
    const threads = groupThreads([{ id: "x1", parent_id: "hidden" }]);
    expect(threads).toEqual([{ root: { id: "x1", parent_id: "hidden" }, replies: [] }]);
  });
});

describe("visibleReplies", () => {
  const replies = ["r1", "r2", "r3", "r4", "r5"];

  it("folds all but the latest replies", () => {
    expect(visibleReplies(replies, false)).toEqual({
      shown: replies.slice(replies.length - FOLDED_REPLIES),
      hidden: replies.length - FOLDED_REPLIES,
    });
  });

  it("shows everything when expanded or short", () => {
    expect(visibleReplies(replies, true)).toEqual({ shown: replies, hidden: 0 });
    expect(visibleReplies(["r1"], false)).toEqual({ shown: ["r1"], hidden: 0 });
  });

  it("folds a resolved thread to the reply that settled it", () => {
    expect(visibleReplies(["r1", "r2", "r3"], false, true)).toEqual({ shown: ["r3"], hidden: 2 });
    expect(visibleReplies(["r1", "r2", "r3"], true, true)).toEqual({
      shown: ["r1", "r2", "r3"],
      hidden: 0,
    });
  });
});

describe("inflightMentions", () => {
  it("lists runs that started and did not finish, with their thread", () => {
    const got = inflightMentions([
      { kind: "mention_started", ref_id: "i1", payload: { agent: "devops", thread_id: "a" } },
      {
        kind: "mention_started",
        ref_id: "i2",
        actor: "agent:reviewer",
        payload: { thread_id: "b" },
      },
      { kind: "mention_started", ref_id: "i3", payload: { agent: "manager" } },
      { kind: "mention_finished", ref_id: "i1" },
      { kind: "stage_changed", ref_id: "i2" },
    ]);
    expect(got).toEqual([
      { agent: "reviewer", thread: "b" },
      { agent: "manager", thread: "" },
    ]);
  });
});

describe("lastAgent", () => {
  it("is the agent that spoke last, or nobody", () => {
    const [th] = groupThreads([
      { id: "r", source: "user", author: "p@x" },
      { id: "1", parent_id: "r", source: "agent", author: "devops" },
      { id: "2", parent_id: "r", source: "system", author: "octopus" },
      { id: "3", parent_id: "r", source: "user", author: "p@x" },
    ]);
    expect(lastAgent(th)).toBe("devops");
    expect(lastAgent({ root: { id: "x", source: "user" }, replies: [] })).toBe("");
  });
});
