import { describe, expect, it, vi } from "vitest";

vi.mock("astro:transitions/client", () => ({ navigate: vi.fn() }));

import { blankStage, flagState, stagePatchLock } from "./board-editor";
import { MIRRORED_RULES, boardProblems, newProblems } from "./board-rules";
import fixtures from "./board-rules.fixtures.json";
import type { Stage } from "./stages";

interface Fixture {
  name: string;
  error: string;
  stages: Array<Partial<Stage>>;
}

const fill = (s: Partial<Stage>): Stage => ({
  ...blankStage(s.id ?? "", s.label ?? "", "#5d6477"),
  ...s,
});

describe("board rules shared with the daemon", () => {
  for (const f of fixtures.cases as Fixture[]) {
    it(f.name, () => {
      const rules = boardProblems(f.stages.map(fill)).map((p) => p.rule);
      if (f.error === "") expect(rules).toEqual([]);
      else expect(rules).toContain(f.error);
    });
  }

  it("cover every mirrored rule with a fixture", () => {
    const covered = new Set((fixtures.cases as Fixture[]).map((f) => f.error));
    for (const rule of MIRRORED_RULES) expect(covered, rule).toContain(rule);
  });

  it("never mirror a rule the fixtures say is checked elsewhere", () => {
    const elsewhere = Object.keys(fixtures.not_mirrored);
    for (const rule of MIRRORED_RULES) expect(elsewhere).not.toContain(rule);
  });
});

const col = (id: string, extra: Partial<Stage> = {}, position = 0): Stage => ({
  ...blankStage(id, id.toUpperCase(), "#5d6477"),
  position,
  ...extra,
});

const flow = (cols: Array<[string, Partial<Stage>]>) =>
  cols.map(([id, extra], i) => col(id, extra, (i + 1) * 10));

describe("newProblems", () => {
  it("ignores problems the board already had", () => {
    const broken = flow([
      ["backlog", { initial: true }],
      ["dev", { merge_target: true }],
      ["done", { terminal: true }],
    ]);
    expect(newProblems(broken, broken)).toEqual([]);
    const worse = broken.map((s) => (s.id === "done" ? { ...s, auto_advance: true } : s));
    expect(newProblems(broken, worse).map((p) => p.rule)).toEqual(["terminal_auto_advance"]);
  });
});

describe("column locks follow the daemon's flow rules", () => {
  const board = flow([
    ["backlog", { initial: true }],
    ["dev", { push_branch: true }],
    ["review", { open_pr: true, changes_kick_back_to: "dev" }],
    ["ship", { merge_target: true }],
    ["done", { terminal: true }],
  ]);

  it("lock turning off the PR opener the merge column needs", () => {
    const st = flagState(board[2], "open_pr", board);
    expect(st.disabled).toBe(true);
    expect(st.reason).toContain("SHIP");
    // A draft PR earlier keeps the merge column fed.
    const draft = board.map((s) => (s.id === "dev" ? { ...s, pr_draft: true } : s));
    expect(flagState(draft[2], "open_pr", draft).disabled).toBe(false);
  });

  it("lock a skip list on a column with approvals, a publishing flag or a kick-back to it", () => {
    expect(stagePatchLock(board, "dev", { skip_unless: ["ui"] })).not.toBe("");
    const quiet = board.map((s) => (s.id === "dev" ? { ...s, push_branch: false } : s));
    // review sends changes back to dev.
    expect(stagePatchLock(quiet, "dev", { skip_unless: ["ui"] })).toContain("REVIEW");
    const gated = quiet.map((s) =>
      s.id === "ship" ? { ...s, required_approvals: [{ key: "qa", label: "QA" }] } : s,
    );
    expect(stagePatchLock(gated, "ship", { skip_unless: ["ui"] })).not.toBe("");
    expect(stagePatchLock(quiet, "backlog", { skip_unless: ["ui"] })).not.toBe("");
  });

  it("lock approvals that would break the flow", () => {
    // A kick-back from a column that frees resources.
    const freeing = board.map((s) => (s.id === "ship" ? { ...s, release_resources: true } : s));
    expect(
      stagePatchLock(freeing, "ship", {
        on_changes_requested: "kick_back",
        changes_kick_back_to: "dev",
      }),
    ).not.toBe("");
    // Required checks before the PR opens.
    expect(
      stagePatchLock(board, "dev", {
        required_approvals: [{ key: "merge", label: "Merge", required_checks: true }],
      }),
    ).not.toBe("");
    expect(
      stagePatchLock(board, "ship", {
        required_approvals: [{ key: "merge", label: "Merge", required_checks: true }],
      }),
    ).toBe("");
  });

  it("take a change that keeps the board valid", () => {
    expect(stagePatchLock(board, "dev", { agent_verdict: true })).toBe("");
    expect(flagState(board[1], "lock_tests", board).disabled).toBe(false);
  });
});
