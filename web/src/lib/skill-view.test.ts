import { describe, expect, it } from "vitest";

import {
  DESCRIPTION_MAX,
  descriptionCount,
  enforcementOf,
  grantStages,
  orderFiles,
} from "./skill-view";

describe("enforcementOf", () => {
  it("reads an empty or unknown value as on demand", () => {
    expect(enforcementOf("")).toBe("on_demand");
    expect(enforcementOf(undefined)).toBe("on_demand");
    expect(enforcementOf("on_demand")).toBe("on_demand");
    expect(enforcementOf("always")).toBe("always");
  });
});

describe("descriptionCount", () => {
  it("counts what the daemon saves, with whitespace folded", () => {
    expect(descriptionCount("  Use when:\n writing  notes ")).toEqual({
      text: "Use when: writing notes",
      count: 23,
      over: false,
    });
  });

  it("flags a description past the limit", () => {
    expect(descriptionCount("x".repeat(DESCRIPTION_MAX + 1)).over).toBe(true);
    expect(descriptionCount("x".repeat(DESCRIPTION_MAX)).over).toBe(false);
  });

  it("counts characters, not UTF-16 units", () => {
    expect(descriptionCount("ñandú \u{1D4B3}").count).toBe(7);
  });
});

describe("grantStages", () => {
  it("names each stage by its label", () => {
    const labels: Record<string, string> = { review: "Review", in_progress: "In progress" };
    expect(grantStages(["in_progress", "review"], (id) => labels[id] ?? id)).toBe(
      "In progress, Review",
    );
    expect(grantStages(undefined, (id) => id)).toBe("");
  });
});

describe("orderFiles", () => {
  it("keeps the live files in order and puts the deleted ones last", () => {
    expect(
      orderFiles([
        { path: "z.md", deleted: true },
        { path: "prompt.md" },
        { path: "a.md", deleted: true },
        { path: "skill.yaml" },
      ]).map((f) => f.path),
    ).toEqual(["prompt.md", "skill.yaml", "a.md", "z.md"]);
  });
});
