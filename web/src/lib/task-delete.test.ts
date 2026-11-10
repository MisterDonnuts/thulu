import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { deleteWithSubtasks, LISTED_SUBTASKS, refusedForSubtasks } from "./task-delete";

describe("deleteWithSubtasks", () => {
  it("names every subtask as an item of the dialog's list", () => {
    expect(deleteWithSubtasks(["API", "UI"])).toEqual({
      message: "These 2 subtasks are deleted with it:",
      details: ["API", "UI"],
      notes: ["Their worktrees are cleaned up too. This cannot be undone."],
    });
  });

  it("counts the subtasks past the listed ones", () => {
    const titles = Array.from({ length: LISTED_SUBTASKS + 3 }, (_, i) => `s${i}`);
    const { details } = deleteWithSubtasks(titles);
    expect(details).toHaveLength(LISTED_SUBTASKS + 1);
    expect(details.slice(0, LISTED_SUBTASKS)).toEqual(titles.slice(0, LISTED_SUBTASKS));
    expect(details.at(-1)).toBe("and 3 more");
  });

  it("lists exactly LISTED_SUBTASKS without a count", () => {
    const titles = Array.from({ length: LISTED_SUBTASKS }, (_, i) => `s${i}`);
    expect(deleteWithSubtasks(titles).details).toEqual(titles);
  });
});

describe("refusedForSubtasks", () => {
  it("spots the daemon's 409 for a parent that still has subtasks", () => {
    const err = new ApiError(409, "This task has subtasks.", {
      code: "api.tasks.errors.delete_subtasks_first",
      subtask_count: 2,
    });
    expect(refusedForSubtasks(err)).toBe(true);
  });

  it("leaves every other failure alone", () => {
    expect(
      refusedForSubtasks(new ApiError(409, "conflict", { code: "api.tasks.errors.other" })),
    ).toBe(false);
    expect(
      refusedForSubtasks(
        new ApiError(403, "forbidden", { code: "api.tasks.errors.delete_subtasks_first" }),
      ),
    ).toBe(false);
    expect(refusedForSubtasks(new ApiError(409, "conflict"))).toBe(false);
    expect(refusedForSubtasks(new TypeError("network"))).toBe(false);
  });
});
