import { describe, expect, it, vi } from "vitest";

vi.mock("astro:transitions/client", () => ({ navigate: vi.fn() }));

import {
  MAX_SKIP_UNLESS,
  approvalsAddLock,
  blankStage,
  flagPatch,
  flagState,
  kickBackChoices,
  parseSkipUnless,
  skipUnlessText,
} from "./board-editor";
import {
  TYPE_AGENT_COLUMN,
  typeAgentStages,
  typeAgentValue,
  walkStage,
  withTypeAgent,
} from "./board-types";
import { MAX_KICK_BACKS, parseKickBacks } from "./boards-settings";
import { columnAgentText, waitsForPerson } from "./column-agent";
import type { Stage, TaskType } from "./stages";

const col = (id: string, extra: Partial<Stage> = {}): Stage => ({
  ...blankStage(id, id.toUpperCase(), "#5d6477"),
  ...extra,
});

const board = [
  col("backlog", { initial: true }),
  col("dev"),
  col("review", { open_pr: true }),
  col("ship", { merge_target: true }),
  col("done", { terminal: true }),
];

describe("flow flags in the column editor", () => {
  it("lock agent flags on the start and finishing columns", () => {
    for (const f of ["agent_verdict", "lock_tests", "release_resources", "pr_draft"] as const) {
      expect(flagState(board[0], f, board).disabled).toBe(true);
      expect(flagState(board[4], f, board).disabled).toBe(true);
      // dev frees nothing: review after it still opens the PR (release_resources_worktree_after).
      expect(flagState(board[1], f, board).disabled).toBe(f === "release_resources");
    }
    expect(flagState(board[0], "pr_ready", board).disabled).toBe(true);
    expect(flagState(board[4], "pr_ready", board).disabled).toBe(false);
  });

  it("allow one merge column per board", () => {
    const st = flagState(board[2], "merge_target", board);
    expect(st.disabled).toBe(true);
    expect(st.reason).toContain("SHIP");
    expect(flagState(board[3], "merge_target", board)).toMatchObject({
      checked: true,
      disabled: false,
    });
  });

  it("keep a skippable column from publishing", () => {
    const skippable = col("design", { skip_unless: ["ui"] });
    expect(flagState(skippable, "push_branch", [skippable]).disabled).toBe(true);
    expect(flagState(skippable, "agent_verdict", [skippable]).disabled).toBe(false);
    // A flag already on stays editable so it can be turned off.
    const both = col("design", { skip_unless: ["ui"], open_pr: true });
    expect(flagState(both, "open_pr", [both]).disabled).toBe(false);
  });

  it("lock what the daemon's flow rules refuse, and say why", () => {
    // Turning off the only PR opener before the merge column.
    const pr = flagState(board[2], "open_pr", board);
    expect(pr.disabled).toBe(true);
    expect(pr.reason).toContain("SHIP");
    // A merge column with no PR before it.
    const early = [
      col("backlog", { initial: true }),
      col("dev"),
      col("review", { open_pr: true }),
      col("done", { terminal: true }),
    ];
    expect(flagState(early[1], "merge_target", early).disabled).toBe(true);
    expect(flagState(early[2], "merge_target", early).disabled).toBe(true);
    // A column after one that freed the worktree cannot push.
    const freed = [
      col("backlog", { initial: true }),
      col("ship", { release_resources: true }),
      col("late"),
      col("done", { terminal: true }),
    ];
    const push = flagState(freed[2], "push_branch", freed);
    expect(push.disabled).toBe(true);
    expect(push.reason).toContain("SHIP");
    expect(flagState(freed[2], "agent_verdict", freed).disabled).toBe(false);
    // A column that sends changes back cannot free resources.
    const back = [
      col("backlog", { initial: true }),
      col("dev"),
      col("ship", { on_changes_requested: "kick_back", changes_kick_back_to: "dev" }),
      col("done", { terminal: true }),
    ];
    expect(flagState(back[2], "release_resources", back).disabled).toBe(true);
  });

  it("clear agent flags when a column becomes the start or the finish", () => {
    const st = col("dev", {
      agent_verdict: true,
      lock_tests: true,
      pr_ready: true,
      merge_target: true,
      skip_unless: ["ui"],
    });
    expect(flagPatch(st, "terminal", true)).toEqual({
      terminal: true,
      agent_verdict: false,
      lock_tests: false,
      skip_unless: [],
    });
    expect(flagPatch(st, "initial", true)).toEqual({
      initial: true,
      agent_verdict: false,
      lock_tests: false,
      pr_ready: false,
      merge_target: false,
      skip_unless: [],
    });
    expect(flagPatch(st, "pr_draft", true)).toEqual({ pr_draft: true });
  });
});

describe("board-wide rules the daemon's validateFlow enforces", () => {
  const flow = [
    col("backlog", { initial: true, position: 10 }),
    col("dev", { push_branch: true, position: 20 }),
    col("verify", { pr_draft: true, position: 30 }),
    col("review", { changes_kick_back_to: "dev", on_changes_requested: "kick_back", position: 40 }),
    col("ship", { merge_target: true, release_resources: true, position: 50 }),
    col("after", { position: 60 }),
    col("done", { terminal: true, position: 70 }),
  ];
  const at = (id: string): Stage => flow.find((s) => s.id === id) ?? col(id);

  it("keep the worktree until no later column needs it", () => {
    // verify drafts the pull request after dev, so dev cannot free the worktree.
    const dev = flagState(at("dev"), "release_resources", flow);
    expect(dev.disabled).toBe(true);
    expect(dev.reason).toContain("VERIFY");
    // review sends changes back to dev.
    expect(flagState(at("review"), "release_resources", flow).disabled).toBe(true);
    expect(flagState(at("after"), "release_resources", flow).disabled).toBe(false);
  });

  it("let only the finishing column push after a release", () => {
    const st = flagState(at("after"), "push_branch", flow);
    expect(st.disabled).toBe(true);
    expect(st.reason).toContain("SHIP");
    expect(flagState(at("done"), "push_branch", flow).disabled).toBe(false);
    expect(flagState(at("verify"), "lock_tests", flow).disabled).toBe(false);
  });

  it("keep the only pull request opener before the merge column on", () => {
    const st = flagState(at("verify"), "pr_draft", flow);
    expect(st).toMatchObject({ checked: true, disabled: true });
    expect(st.reason).toContain("SHIP");
    const both = flow.map((s) => (s.id === "verify" ? { ...s, open_pr: true } : s));
    const verify = both.find((s) => s.id === "verify") ?? col("verify");
    expect(flagState(verify, "pr_draft", both).disabled).toBe(false);
    // Becoming the start or the finish clears that pr_draft (flagPatch).
    expect(flagState(at("verify"), "terminal", flow).reason).toContain("SHIP");
    expect(flagState(at("verify"), "initial", flow).disabled).toBe(true);
    expect(flagState(at("after"), "terminal", flow).disabled).toBe(false);
  });

  it("refuse a first approval on a column that may be skipped", () => {
    const skippable = col("design", { skip_unless: ["ui"], position: 25 });
    expect(approvalsAddLock([...flow, skippable], skippable)).not.toBe("");
    expect(approvalsAddLock(flow, at("after"))).toBe("");
  });

  it("offer no skippable column as kick-back target", () => {
    const skippable = col("design", { skip_unless: ["ui"], position: 25 });
    const board = [...flow, skippable].sort((x, y) => x.position - y.position);
    const ids = (st: Stage) => kickBackChoices(board, st).map((s) => s.id);
    expect(ids(at("after"))).not.toContain("design");
    expect(ids(at("after"))).toContain("verify");
    // A target already set stays listed, so the select shows it.
    const pinned = { ...at("after"), changes_kick_back_to: "design" };
    expect(kickBackChoices(board, pinned).map((s) => s.id)).toContain("design");
  });
});

describe("parseSkipUnless", () => {
  it("splits, lowercases and drops repeats", () => {
    expect(parseSkipUnless(" UI, api  api\nmobile ")).toEqual({ values: ["ui", "api", "mobile"] });
    expect(parseSkipUnless("")).toEqual({ values: [] });
  });

  it("refuses values the daemon refuses", () => {
    expect(parseSkipUnless("ui, front-end")).toHaveProperty("problem");
    const many = Array.from({ length: MAX_SKIP_UNLESS + 1 }, (_, i) => `v${i}`).join(",");
    expect(parseSkipUnless(many)).toHaveProperty("problem");
  });

  it("prints back as a list", () => {
    expect(skipUnlessText(col("x", { skip_unless: ["ui", "api"] }))).toBe("ui, api");
    expect(skipUnlessText(col("x"))).toBe("");
  });
});

describe("agents per task type", () => {
  const tt: TaskType = {
    id: "release",
    label: "Release",
    position: 10,
    skips: ["review"],
    agents: { dev: "", ship: "releaser" },
  };

  it("offer only the columns the type walks and an agent runs in", () => {
    expect(typeAgentStages(board, tt).map((s) => s.id)).toEqual(["dev", "ship"]);
  });

  it("tell the column's agent from no agent", () => {
    expect(typeAgentValue(tt, "dev")).toBe("");
    expect(typeAgentValue(tt, "ship")).toBe("releaser");
    expect(typeAgentValue(tt, "review")).toBe(TYPE_AGENT_COLUMN);
    expect(typeAgentValue({ agents: null }, "dev")).toBe(TYPE_AGENT_COLUMN);
  });

  it("drop the entry when set back to the column's agent", () => {
    expect(withTypeAgent(tt.agents, "ship", TYPE_AGENT_COLUMN)).toEqual({ dev: "" });
    expect(withTypeAgent(null, "dev", "coder")).toEqual({ dev: "coder" });
  });

  it("drop the agent of a column the type stops walking", () => {
    expect(walkStage(tt, "ship", false)).toMatchObject({
      skips: ["review", "ship"],
      agents: { dev: "" },
    });
    expect(walkStage(tt, "review", true)).toMatchObject({ skips: [], agents: tt.agents });
  });
});

describe("column default agent", () => {
  const nobody = { agents: [], with: 0, projects: 2 };
  it("covers the column when no project names one", () => {
    expect(columnAgentText(col("dev", { default_agent: "coder" }), nobody)).toContain("coder");
    expect(waitsForPerson(nobody, { default_agent: "coder" })).toBe(false);
    expect(waitsForPerson(nobody)).toBe(true);
  });
});

describe("parseKickBacks", () => {
  it("reads 0 to the limit and empty as no limit", () => {
    expect(parseKickBacks("")).toBe(0);
    expect(parseKickBacks(" 3 ")).toBe(3);
    expect(parseKickBacks(String(MAX_KICK_BACKS))).toBe(MAX_KICK_BACKS);
    expect(parseKickBacks(String(MAX_KICK_BACKS + 1))).toBeNull();
    expect(parseKickBacks("-1")).toBeNull();
    expect(parseKickBacks("2.5")).toBeNull();
  });
});
