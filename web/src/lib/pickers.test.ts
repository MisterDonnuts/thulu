import { describe, expect, it } from "vitest";
import {
  agentColumns,
  agentCovers,
  boardOptions,
  readStageTemplate,
  stageAgentRows,
  type AgentType,
} from "./pickers";
import type { Board, Stage } from "./stages";

const boards: Board[] = [
  { id: "b1", label: "Default" },
  { id: "b2", label: "Research" },
] as Board[];

const stages: Stage[] = [
  { id: "backlog", label: "Backlog", initial: true },
  { id: "build", label: "Build" },
  { id: "review", label: "Review" },
  { id: "done", label: "Done", terminal: true },
] as unknown as Stage[];

const agents: AgentType[] = [
  { name: "coder", type: "coder" },
  { name: "reviewer", type: "reviewer", valid_stages: ["review"] },
];

describe("boardOptions", () => {
  it("puts the instance default on top", () => {
    expect(boardOptions(boards, "")).toMatch(/^<option value="">— instance default —<\/option>/);
  });
  it("marks the selected board", () => {
    const html = boardOptions(boards, "b2");
    expect(html).toContain('<option value="b2" selected>Research</option>');
    expect(html).toContain('<option value="b1">Default</option>');
  });
  it("escapes a label", () => {
    expect(boardOptions([{ id: "x", label: "<b>" }] as Board[], "")).not.toContain("<b>");
  });
});

describe("agentCovers", () => {
  it("reads no valid stages as every stage", () => {
    expect(agentCovers({}, "build")).toBe(true);
    expect(agentCovers({ valid_stages: [] }, "build")).toBe(true);
    expect(agentCovers({ valid_stages: null }, "build")).toBe(true);
  });
  it("keeps a listed agent to its stages", () => {
    expect(agentCovers({ valid_stages: ["review"] }, "review")).toBe(true);
    expect(agentCovers({ valid_stages: ["review"] }, "build")).toBe(false);
  });
});

describe("agentColumns", () => {
  it("leaves out where tasks start and end, and the virtual stages", () => {
    const list = [...stages, { id: "chat", label: "Chat", virtual: true } as Stage];
    expect(agentColumns(list).map((s) => s.id)).toEqual(["build", "review"]);
  });
});

describe("stageAgentRows", () => {
  it("lists every column between start and end", () => {
    const html = stageAgentRows(stages, agents, {});
    expect(html.match(/<tr>/g)?.length).toBe(2);
    expect(html).not.toContain("Done");
    expect(html).not.toContain("Backlog");
  });
  it("names each select after its column", () => {
    const html = stageAgentRows(stages, agents, {});
    for (const s of agentColumns(stages)) {
      expect(html).toContain(`data-stage="${s.id}" aria-label="${s.label}"`);
    }
  });
  it("keeps a row for a template entry the board does not show", () => {
    const html = stageAgentRows(
      stages,
      agents,
      { qa: "coder" },
      {
        catalog: [...stages, { id: "qa", label: "QA" } as Stage],
      },
    );
    expect(html).toContain("QA");
    expect(html).toContain('data-stage="qa"');
  });
  it("says so, with a link, when no column can run an agent", () => {
    const html = stageAgentRows(
      [stages[0], stages[3]],
      agents,
      {},
      { boardHref: "/settings/#boards" },
    );
    expect(html).toContain("tpl-empty");
    expect(html).toContain('href="/settings/#boards"');
    expect(html).not.toContain("<select");
  });
  it("offers an agent only where it is eligible", () => {
    const html = stageAgentRows(stages, agents, {});
    const build = html.slice(0, html.indexOf("Review"));
    expect(build).toContain(">coder<");
    expect(build).not.toContain(">reviewer<");
  });
  it("treats an agent with no valid_stages as eligible everywhere", () => {
    expect(stageAgentRows(stages, agents, {}).match(/>coder</g)?.length).toBe(2);
  });
  it("marks the currently assigned agent", () => {
    expect(stageAgentRows(stages, agents, { build: "coder" })).toContain(
      '<option value="coder" selected>coder</option>',
    );
  });
});

describe("readStageTemplate", () => {
  it("reads the chosen agent per stage", () => {
    expect(
      readStageTemplate([
        { value: "coder", dataset: { stage: "build" } },
        { value: "reviewer", dataset: { stage: "review" } },
      ]),
    ).toEqual({ build: "coder", review: "reviewer" });
  });
  it("leaves a stage on '—' out rather than storing an empty default", () => {
    expect(
      readStageTemplate([
        { value: "", dataset: { stage: "build" } },
        { value: "coder", dataset: { stage: "review" } },
      ]),
    ).toEqual({ review: "coder" });
  });
  it("round-trips what stageAgentRows rendered", () => {
    const template = { build: "coder", review: "reviewer" };
    const selects = agentColumns(stages).map((s) => ({
      value: template[s.id as keyof typeof template],
      dataset: { stage: s.id },
    }));
    expect(readStageTemplate(selects)).toEqual(template);
  });
  it("is empty for an empty table", () => {
    expect(readStageTemplate([])).toEqual({});
  });
});
