import { describe, expect, it } from "vitest";
import {
  MIXED,
  boardProjects,
  columnAgent,
  columnAgentText,
  columnToOffer,
  pickerValue,
  saveColumnAgent,
  takesAgent,
  templateWith,
  validStagesWith,
  waitsForPerson,
  type TemplateProject,
} from "./column-agent";
import type { Stage } from "./stages";

const projects: TemplateProject[] = [
  { id: "a", name: "A", default_board_id: "", stage_template: { build: "coder" } },
  { id: "b", name: "B", default_board_id: "main", stage_template: { build: "coder" } },
  { id: "c", name: "C", default_board_id: "research", stage_template: { build: "writer" } },
  { id: "d", name: "D", default_board_id: "main", stage_template: null },
];

const mid = { id: "build", label: "Build" } as Stage;

describe("boardProjects", () => {
  it("takes the projects that name the board, and on the default one those that name none", () => {
    expect(boardProjects(projects, "main", "main").map((p) => p.id)).toEqual(["a", "b", "d"]);
    expect(boardProjects(projects, "research", "main").map((p) => p.id)).toEqual(["c"]);
  });
});

describe("columnAgent and pickerValue", () => {
  it("shows one agent when every project runs it", () => {
    const state = columnAgent(projects.slice(0, 2), "build");
    expect(state).toEqual({ agents: ["coder"], with: 2, projects: 2 });
    expect(pickerValue(state)).toBe("coder");
  });
  it("is mixed when a project runs another agent or none", () => {
    expect(pickerValue(columnAgent(projects, "build"))).toBe(MIXED);
    expect(pickerValue(columnAgent([projects[0], projects[3]], "build"))).toBe(MIXED);
  });
  it("is none when no project names one", () => {
    expect(pickerValue(columnAgent(projects, "review"))).toBe("");
  });
});

describe("columnAgentText", () => {
  it("names the agent, the mix, or the gap", () => {
    expect(columnAgentText(mid, columnAgent(projects.slice(0, 2), "build"))).toContain("coder");
    expect(columnAgentText(mid, columnAgent(projects, "build"))).toContain("coder, writer");
    expect(columnAgentText(mid, columnAgent(projects, "review"))).not.toBe("");
  });
  it("says nothing for start and end columns", () => {
    const state = columnAgent(projects, "build");
    expect(columnAgentText({ ...mid, initial: true }, state)).toBe("");
    expect(columnAgentText({ ...mid, terminal: true }, state)).toBe("");
    expect(takesAgent({ initial: false, terminal: true })).toBe(false);
    expect(takesAgent({ initial: false, terminal: false })).toBe(true);
  });
});

describe("waitsForPerson", () => {
  it("only when no project of the board staffs the column", () => {
    expect(waitsForPerson(columnAgent(projects, "review"))).toBe(true);
    expect(waitsForPerson(columnAgent(projects, "build"))).toBe(false);
    expect(waitsForPerson(columnAgent([], "build"))).toBe(true);
  });
});

describe("templateWith", () => {
  it("sets and clears one column, leaving the rest", () => {
    expect(templateWith({ build: "coder", review: "r" }, "build", "x")).toEqual({
      build: "x",
      review: "r",
    });
    expect(templateWith({ build: "coder", review: "r" }, "build", "")).toEqual({ review: "r" });
    expect(templateWith(null, "build", "x")).toEqual({ build: "x" });
  });
});

describe("saveColumnAgent", () => {
  it("writes only the projects that change, and reports the ones that fail", async () => {
    const calls: Array<[string, unknown]> = [];
    const { result, projects: out } = await saveColumnAgent(
      projects,
      "build",
      "coder",
      (id, body) => {
        calls.push([id, body]);
        if (id === "d") return Promise.reject(new Error("boom"));
        const p = projects.find((x) => x.id === id) ?? { id, name: id };
        return Promise.resolve({ ...p, ...body });
      },
    );
    expect(calls.map((c) => c[0])).toEqual(["c", "d"]);
    expect(result.saved).toBe(1);
    expect(result.failed).toEqual([{ project: "D", error: "boom" }]);
    expect(out.find((p) => p.id === "c")?.stage_template).toEqual({ build: "coder" });
    expect(out.find((p) => p.id === "d")?.stage_template).toBeNull();
  });
});

describe("columnToOffer and validStagesWith", () => {
  const stages = [
    { id: "backlog", initial: true, terminal: false },
    { id: "build", initial: false, terminal: false },
    { id: "review", initial: false, terminal: false },
    { id: "done", initial: false, terminal: true },
  ];
  it("offers the first middle column a project leaves without an agent", () => {
    const ps: TemplateProject[] = [{ id: "a", name: "A", stage_template: { build: "x" } }];
    expect(columnToOffer(stages, ps)?.id).toBe("review");
    expect(columnToOffer(stages, [])?.id).toBe("build");
    expect(columnToOffer([stages[0], stages[3]], ps)).toBeUndefined();
  });
  it("adds the column only to an agent that lists its stages", () => {
    expect(validStagesWith([], "build")).toEqual([]);
    expect(validStagesWith(["review"], "build")).toEqual(["review", "build"]);
    expect(validStagesWith(["build"], "build")).toEqual(["build"]);
  });
});
