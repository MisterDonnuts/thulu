import { describe, expect, it } from "vitest";

import { TYPE_TEMPLATES, isLockedStage, newType, toggleSkip, validateNewType } from "./board-types";
import type { TaskType } from "./stages";

function type(id: string, skips: string[] = []): TaskType {
  return { id, label: id, position: 10, skips };
}

describe("isLockedStage", () => {
  it("locks the columns that start and finish the board", () => {
    expect(isLockedStage({ initial: true, terminal: false })).toBe(true);
    expect(isLockedStage({ initial: false, terminal: true })).toBe(true);
    expect(isLockedStage({ initial: false, terminal: false })).toBe(false);
  });
});

describe("toggleSkip", () => {
  it("drops a column from the skip list when the type walks it", () => {
    expect(toggleSkip(["review", "qa"], "review", true)).toEqual(["qa"]);
  });

  it("adds a column the type stops walking", () => {
    expect(toggleSkip(["qa"], "review", false)).toEqual(["qa", "review"]);
  });

  it("never lists the same column twice", () => {
    expect(toggleSkip(["review"], "review", false)).toEqual(["review"]);
  });

  it("leaves the list it was given alone", () => {
    const skips = ["review"];
    toggleSkip(skips, "qa", false);
    expect(skips).toEqual(["review"]);
  });
});

describe("newType", () => {
  it("positions the row after the ones already in the table", () => {
    expect(newType({ id: "bugfix", label: "Bugfix" }, 2).position).toBe(30);
  });

  it("starts walking the whole board", () => {
    expect(newType({ id: "bugfix", label: "Bugfix" }, 0).skips).toEqual([]);
  });

  it("carries the template's icon and colour", () => {
    const t = newType(TYPE_TEMPLATES[1], 0);
    expect([t.icon, t.color]).toEqual(["bug", "#e5484d"]);
  });
});

describe("validateNewType", () => {
  it("passes a new id with a label", () => {
    expect(validateNewType("bugfix", "Bugfix", [])).toBeNull();
  });

  it("requires both halves", () => {
    expect(validateNewType("", "Bugfix", [])).toBe("Both an id and a label are required");
    expect(validateNewType("bugfix", "", [])).toBe("Both an id and a label are required");
  });

  it("refuses an id this board already offers", () => {
    expect(validateNewType("bugfix", "Bugfix", [type("bugfix")])).toBe(
      "This board already offers bugfix",
    );
  });
});
