import { describe, expect, it } from "vitest";

import { agentLink, deepLinkAgent, parseAgentLink } from "./agent-link";

describe("agentLink", () => {
  it("names the agent and the tab", () => {
    expect(agentLink("developer", "skills")).toBe("/agents/?agent=developer#skills");
  });

  it("leaves out what it is not given", () => {
    expect(agentLink(undefined, "mcp")).toBe("/agents/#mcp");
    expect(agentLink("dev ops")).toBe("/agents/?agent=dev%20ops");
  });
});

describe("parseAgentLink", () => {
  it("reads back what agentLink builds", () => {
    expect(parseAgentLink("?agent=developer", "#skills")).toEqual({
      agent: "developer",
      tab: "skills",
    });
  });

  it("ignores a tab the editor does not have", () => {
    expect(parseAgentLink("", "#history")).toEqual({ agent: undefined, tab: undefined });
  });
});

describe("deepLinkAgent", () => {
  const listed = ["backend", "developer"];

  it("opens the agent the link names", () => {
    expect(deepLinkAgent({ agent: "developer", tab: "skills" }, listed)).toBe("developer");
  });

  it("opens the first agent when the link has only a tab", () => {
    expect(deepLinkAgent({ tab: "mcp" }, listed)).toBe("backend");
  });

  it("falls back to the first agent when the named one is gone", () => {
    expect(deepLinkAgent({ agent: "removed", tab: "mcp" }, listed)).toBe("backend");
  });

  it("opens nothing without a tab or a known agent", () => {
    expect(deepLinkAgent({ agent: "removed" }, listed)).toBeUndefined();
    expect(deepLinkAgent({}, listed)).toBeUndefined();
    expect(deepLinkAgent({ tab: "mcp" }, [])).toBeUndefined();
  });
});
