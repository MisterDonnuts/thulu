import { describe, expect, it } from "vitest";
import { parseTags, preferredProject } from "./new-task";

describe("parseTags", () => {
  it("splits on commas and trims", () => {
    expect(parseTags(" frontend, bug ,checkout")).toEqual(["frontend", "bug", "checkout"]);
  });

  it("drops empty tags and repeats", () => {
    expect(parseTags("bug,, bug ,")).toEqual(["bug"]);
    expect(parseTags("")).toEqual([]);
  });
});

describe("preferredProject", () => {
  it("offers the remembered repo while it exists", () => {
    expect(preferredProject("p2", ["p1", "p2"])).toBe("p2");
  });

  it("falls back to no repo", () => {
    expect(preferredProject(null, ["p1"])).toBe("");
    expect(preferredProject("gone", ["p1"])).toBe("");
  });
});
