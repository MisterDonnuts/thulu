import { describe, expect, it } from "vitest";
import {
  answeringStage,
  defaultRunAgent,
  missingLine,
  runAgents,
  runCasesBlock,
  runCasesComment,
  type RunAgentSource,
} from "./test-case-run";

describe("who can run a task's cases", () => {
  const types: RunAgentSource[] = [
    {
      type: "qa",
      name: "QA",
      valid_stages: ["mention"],
      mcp_by_stage: { mention: ["thulu-browser@write"] },
    },
    { type: "pm", name: "PM", valid_stages: ["chat"], mcp_by_stage: { chat: ["octopus-api"] } },
    { type: "builder", name: "Builder", valid_stages: ["build"] },
  ];

  it("answers on the mention stage, or from the chat", () => {
    expect(answeringStage({ valid_stages: ["mention", "chat"] })).toBe("mention");
    expect(answeringStage({ valid_stages: ["chat"] })).toBe("chat");
    expect(answeringStage({})).toBe("mention");
    expect(answeringStage({ valid_stages: ["build"] })).toBe("");
  });

  it("says what each agent lacks on the stage that answers", () => {
    expect(runAgents(types)).toEqual([
      { type: "qa", name: "QA", stage: "mention", missing: [] },
      { type: "pm", name: "PM", stage: "chat", missing: ["thulu-browser"] },
    ]);
  });

  it("blocks with the reason", () => {
    expect(runCasesBlock(runAgents(types))).toBe("");
    expect(runCasesBlock(runAgents(types.slice(1)))).toBe("no_browser");
    expect(runCasesBlock([])).toBe("no_agent");
  });

  it("starts on the stage's agent when it can run them", () => {
    const agents = runAgents(types);
    expect(defaultRunAgent(agents, "qa")).toBe("qa");
    expect(defaultRunAgent(agents, "pm")).toBe("qa");
    expect(defaultRunAgent(agents.slice(1), "")).toBe("pm");
    expect(missingLine(agents[1])).toContain("thulu-browser");
    expect(missingLine(agents[0])).toBe("");
  });

  it("asks by mention, with the compare address when there is one", () => {
    const c = runCasesComment({
      agent: "qa",
      url: "https://s.example",
      compare: "",
      keys: ["TC-1", "TC-2"],
    });
    expect(c.startsWith("@qa ")).toBe(true);
    expect(c).toContain("TC-1, TC-2");
    expect(c).not.toContain("compare_base_url");
    const cmp = runCasesComment({
      agent: "qa",
      url: "https://s.example",
      compare: "https://p.example",
      keys: ["TC-1"],
    });
    expect(cmp).toContain("https://p.example");
    expect(cmp).toContain("compare_base_url");
  });

  it("folds the tool instructions under the request", () => {
    const c = runCasesComment({
      agent: "qa",
      url: "https://s.example",
      compare: "",
      keys: ["TC-1"],
    });
    const [ask, rest] = c.split("\n\n<details>");
    expect(ask).toBe("@qa run test cases TC-1 of this task on https://s.example.");
    expect(rest).toMatch(
      /^<summary>Instructions for the agent<\/summary>\n\n.*evidence_publish.*\n\n<\/details>$/s,
    );
  });
});
