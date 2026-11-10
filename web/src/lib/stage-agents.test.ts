import { describe, expect, it } from "vitest";
import {
  dropHint,
  holdsForUndo,
  mergeTemplates,
  newTaskAgentsText,
  nextAgent,
  staffedColumns,
  templateFill,
} from "./stage-agents";
import type { Stage } from "./stages";

const stages = [
  { id: "backlog", label: "Backlog", initial: true },
  { id: "build", label: "Build" },
  { id: "review", label: "Review" },
  { id: "done", label: "Done", terminal: true },
] as unknown as Stage[];

describe("staffedColumns", () => {
  it("joins the board's tasks and the projects' templates", () => {
    const got = staffedColumns(
      [{ stage_assignments: { build: "coder", review: "" } }, { stage_assignments: null }],
      [{ review: "checker" }, null],
    );
    expect([...got].sort()).toEqual(["build", "review"]);
  });
});

describe("nextAgent", () => {
  const staffed = new Set(["build"]);
  it("names the agent the next column starts", () => {
    expect(
      nextAgent({ stage: "backlog", stage_assignments: { build: "coder" } }, stages, staffed),
    ).toEqual({ stage: "build", label: "Build", agent: "coder" });
  });
  it("warns only when the board staffs the column", () => {
    expect(nextAgent({ stage: "backlog" }, stages, staffed)?.agent).toBe("");
    expect(nextAgent({ stage: "build" }, stages, staffed)).toBeNull();
  });
  it("says nothing before a finishing column or at the end", () => {
    expect(nextAgent({ stage: "review" }, stages, new Set(["done"]))).toBeNull();
    expect(nextAgent({ stage: "done" }, stages, staffed)).toBeNull();
  });
  it("skips the columns the task's type skips", () => {
    const got = nextAgent(
      { stage: "backlog", stage_assignments: { review: "checker" } },
      stages,
      staffed,
      new Set(["build"]),
    );
    expect(got).toEqual({ stage: "review", label: "Review", agent: "checker" });
  });
});

describe("dropHint", () => {
  it("names the agent, or says none, and stays quiet on start and end", () => {
    expect(dropHint({ stage_assignments: { build: "coder" } }, stages[1])).toContain("coder");
    expect(dropHint({}, stages[1])).not.toBe("");
    expect(dropHint({ stage_assignments: { done: "x" } }, stages[3])).toBe("");
    expect(dropHint({}, stages[0])).toBe("");
  });
  it("promises no agent for a card a blocking dependency holds", () => {
    const blocked = dropHint({ stage_assignments: { build: "coder" } }, stages[1], true);
    expect(blocked).not.toContain("coder");
    expect(blocked).toBe(dropHint({}, stages[3], true));
    expect(blocked).not.toBe("");
  });
});

describe("holdsForUndo", () => {
  it("holds only a drop that starts an agent on a card nothing blocks", () => {
    expect(holdsForUndo("coder", false)).toBe(true);
    expect(holdsForUndo("", false)).toBe(false);
    expect(holdsForUndo("coder", true)).toBe(false);
  });
});

describe("templateFill", () => {
  it("takes only the columns the task leaves empty", () => {
    expect(
      templateFill({ build: "mine" }, { build: "coder", review: "checker", done: "x" }, [
        "build",
        "review",
      ]),
    ).toEqual({ review: "checker" });
    expect(templateFill(null, null, ["build"])).toEqual({});
  });
});

describe("mergeTemplates and newTaskAgentsText", () => {
  it("lets the first project to name a column win", () => {
    expect(mergeTemplates([{ build: "a" }, { build: "b", review: "c" }])).toEqual({
      build: "a",
      review: "c",
    });
  });
  it("lists the copied agents in board order, or says why there are none", () => {
    const text = newTaskAgentsText(
      [{ name: "P", stage_template: { review: "checker", build: "coder" } }],
      stages,
    );
    expect(text.indexOf("Build")).toBeLessThan(text.indexOf("Review"));
    expect(newTaskAgentsText([{ name: "Empty", stage_template: {} }], stages)).toContain("Empty");
    expect(newTaskAgentsText([], stages)).not.toBe("");
  });
});
