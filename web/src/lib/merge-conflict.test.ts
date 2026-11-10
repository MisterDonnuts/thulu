import { describe, expect, it } from "vitest";

import { agentCanResolve, conflictComment, redoBody } from "./merge-conflict";

describe("redoBody", () => {
  it("keeps the task's body and adds the files under it", () => {
    const out = redoBody("Fix the login.\n", "main", ["src/a.ts", "b`.go"]);
    expect(out.startsWith("Fix the login.\n\n---\n\n")).toBe(true);
    expect(out).toContain("main");
    expect(out).toContain("- `src/a.ts`");
    expect(out).toContain("- `b.go`");
  });
  it("is only the note for a task with no body", () => {
    const out = redoBody("  ", "dev", ["x"]);
    expect(out.startsWith("---")).toBe(false);
    expect(out).toContain("- `x`");
  });
});

describe("conflictComment", () => {
  it("names the target and lists the files", () => {
    const out = conflictComment("main", ["a", "b"]);
    expect(out).toContain("main");
    expect(out.endsWith("- `a`\n- `b`")).toBe(true);
  });
});

describe("agentCanResolve", () => {
  it("is true only when the daemon says the agent can", () => {
    expect(agentCanResolve({ conflicts: ["a"], agent_can_resolve: true })).toBe(true);
    expect(agentCanResolve({ conflicts: ["a"], agent_can_resolve: false })).toBe(false);
    expect(agentCanResolve({ conflicts: ["a"] })).toBe(false);
    expect(agentCanResolve({ agent_can_resolve: "true" })).toBe(false);
    expect(agentCanResolve(null)).toBe(false);
    expect(agentCanResolve("x")).toBe(false);
  });
});
