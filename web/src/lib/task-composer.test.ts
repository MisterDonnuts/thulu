import { describe, expect, it } from "vitest";
import { TaskComposers } from "./task-composer";

describe("TaskComposers", () => {
  it("keeps the draft and the interrupt tick under the task they were written for", () => {
    const c = new TaskComposers();
    c.setDraft("a", "seguí con el redondeo");
    c.setInterrupt("a", true);
    expect(c.draft("b")).toBe("");
    expect(c.interrupt("b")).toBe(false);
    expect(c.draft("a")).toBe("seguí con el redondeo");
    expect(c.interrupt("a")).toBe(true);
  });

  it("keeps interrupt on after the draft is sent", () => {
    const c = new TaskComposers();
    c.setInterrupt("a", true);
    c.setDraft("a", "x");
    c.setDraft("a", "");
    expect(c.interrupt("a")).toBe(true);
    c.setInterrupt("a", false);
    expect(c.interrupt("a")).toBe(false);
    expect(c.draft("a")).toBe("");
  });
});
