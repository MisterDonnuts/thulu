import { describe, expect, it } from "vitest";

import { agentHref, boardHref, projectHref, taskHref } from "./links";

describe("links", () => {
  it("opens a task on its board, at the page and not the API", () => {
    expect(taskHref("a1b2", "web")).toBe("/tasks/?board=web#task-a1b2");
    expect(taskHref("a1b2")).toBe("/tasks/#task-a1b2");
  });

  it("escapes what goes into the query", () => {
    expect(boardHref("a b&c")).toBe("/tasks/?board=a%20b%26c");
    expect(projectHref("p/1")).toBe("/project/?id=p%2F1");
    expect(agentHref("qa bot")).toBe("/agents/?agent=qa%20bot");
  });
});
