import { describe, expect, it } from "vitest";

import {
  firstStepItems,
  orderSteps,
  rateSummary,
  showFirstSteps,
  workspaceSteps,
  type SetupStep,
} from "./dashboard";

describe("rateSummary", () => {
  it("says there is no data instead of 0% when the engine reported no limit", () => {
    for (const rl of [undefined, null, { utilization: Number.NaN }]) {
      expect(rateSummary(rl)).toEqual({ value: "—", band: "", sub: "no data from the engine" });
    }
  });

  it("bands the percentage", () => {
    expect(rateSummary({ utilization: 0 })).toMatchObject({ value: "0%", band: "low" });
    expect(rateSummary({ utilization: 0.6 })).toMatchObject({ value: "60%", band: "mid" });
    expect(rateSummary({ utilization: 0.9 })).toMatchObject({
      value: "90%",
      band: "high",
      sub: "near limit",
    });
  });
});

const step = (id: string, done: boolean): SetupStep => ({ id, title: id, done });

describe("showFirstSteps", () => {
  const pending = { complete: false, steps: [step("llm_key", false), step("agents", true)] };

  it("shows on an empty workspace with steps left", () => {
    expect(showFirstSteps(pending, 0)).toBe(true);
  });

  it("stays away once there is work on the board", () => {
    expect(showFirstSteps(pending, 3)).toBe(false);
  });

  it("stays away when the checklist is done or unreadable", () => {
    expect(showFirstSteps({ complete: true, steps: [step("a", true)] }, 0)).toBe(false);
    expect(showFirstSteps(null, 0)).toBe(false);
    expect(showFirstSteps({ complete: false, steps: [step("a", true)] }, 0)).toBe(false);
  });

  it("does not show for install steps alone, which nobody in the workspace can finish", () => {
    const install = { ...step("identity", false), install: true };
    expect(showFirstSteps({ complete: false, steps: [install, step("llm_key", true)] }, 0)).toBe(
      false,
    );
  });
});

describe("workspaceSteps", () => {
  it("leaves out the install's steps and keeps the order", () => {
    const got = workspaceSteps([
      { ...step("identity", false), install: true },
      { ...step("workspace", true), install: true },
      step("llm_key", false),
      step("agents", true),
    ]);
    expect(got.map((s) => s.id)).toEqual(["llm_key", "agents"]);
  });
});

describe("orderSteps", () => {
  it("puts what is left first and keeps the checklist's order", () => {
    const got = orderSteps([step("a", true), step("b", false), step("c", true), step("d", false)]);
    expect(got.map((s) => s.id)).toEqual(["b", "d", "a", "c"]);
  });
});

describe("firstStepItems", () => {
  it("links a step left and says what is missing until it is done", () => {
    const [got] = firstStepItems([
      { id: "llm_key", title: "Add a key", done: false, href: "/settings/", effect: "no runs" },
    ]);
    expect(got).toEqual({
      title: "Add a key",
      state: "pending",
      href: "/settings/",
      note: "Until then: no runs",
    });
  });

  it("leaves a finished step without a link and keeps what it found", () => {
    const [got] = firstStepItems([
      { id: "agents", title: "Agents", done: true, href: "/agents/", detail: "3 agents" },
    ]);
    expect(got).toEqual({ title: "Agents", state: "done", detail: "3 agents" });
  });

  it("writes no note or detail when the step has none", () => {
    const got = firstStepItems([step("a", false), step("b", true)]);
    expect(got).toEqual([
      { title: "a", state: "pending", href: undefined, note: undefined },
      { title: "b", state: "done", detail: undefined },
    ]);
  });
});
