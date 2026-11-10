import { describe, it, expect } from "vitest";
import {
  harvestSystemSkillsOff,
  stageWiresMCP,
  systemSkillRows,
  type SystemSkillInfo,
} from "./system-skills";

const catalog: SystemSkillInfo[] = [
  { name: "octopus-run", mcp: "", enforcement: "always" },
  { name: "octopus-task-workflow", mcp: "octopus-api" },
  { name: "octopus-browser-evidence", mcp: "thulu-browser" },
];

describe("stageWiresMCP", () => {
  it("reads the server out of scoped and narrowed entries", () => {
    const entries = ["octopus-api@write+task_create", " thulu-github "];
    expect(stageWiresMCP(entries, "octopus-api")).toBe(true);
    expect(stageWiresMCP(entries, "thulu-github")).toBe(true);
    expect(stageWiresMCP(entries, "thulu-browser")).toBe(false);
  });

  it("treats a skill tied to no MCP as wired everywhere", () => {
    expect(stageWiresMCP([], "")).toBe(true);
  });
});

describe("systemSkillRows", () => {
  it("marks what the stage wires and what was turned off", () => {
    const rows = systemSkillRows(catalog, ["octopus-api@write"], ["octopus-run"]);
    expect(rows.map((r) => [r.name, r.wired, r.enabled])).toEqual([
      ["octopus-run", true, false],
      ["octopus-task-workflow", true, true],
      ["octopus-browser-evidence", false, true],
    ]);
  });
});

describe("harvestSystemSkillsOff", () => {
  it("turns unticked boxes into opt-outs, sorted", () => {
    const got = harvestSystemSkillsOff(
      {},
      [
        { stage: "review", name: "octopus-task-workflow", on: false },
        { stage: "review", name: "octopus-run", on: false },
        { stage: "in_progress", name: "octopus-run", on: true },
      ],
      ["in_progress", "review"],
    );
    expect(got).toEqual({ review: ["octopus-run", "octopus-task-workflow"] });
  });

  it("ticking a box back on removes the opt-out", () => {
    const got = harvestSystemSkillsOff(
      { review: ["octopus-run"] },
      [{ stage: "review", name: "octopus-run", on: true }],
      ["review"],
    );
    expect(got).toEqual({});
  });

  it("keeps opt-outs the form did not render", () => {
    const got = harvestSystemSkillsOff(
      { review: ["octopus-gone", "octopus-run"], in_progress: ["octopus-run"] },
      [{ stage: "review", name: "octopus-run", on: false }],
      ["in_progress", "review"],
    );
    expect(got).toEqual({ in_progress: ["octopus-run"], review: ["octopus-gone", "octopus-run"] });
  });

  it("drops the opt-outs of a stage the agent no longer runs", () => {
    const got = harvestSystemSkillsOff(
      { testing: ["octopus-run"] },
      [{ stage: "testing", name: "octopus-run", on: false }],
      ["review"],
    );
    expect(got).toEqual({});
  });

  it("returns stages in sorted order so the dirty check stays stable", () => {
    const got = harvestSystemSkillsOff(
      { review: ["octopus-gone"] },
      [{ stage: "in_progress", name: "octopus-run", on: false }],
      ["in_progress", "review"],
    );
    expect(Object.keys(got)).toEqual(["in_progress", "review"]);
  });
});
