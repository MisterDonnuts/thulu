import { describe, expect, it } from "vitest";

import { codeOnlyOnRunner, placeBody } from "./project-runner-place";

describe("placeBody", () => {
  it("drops the folder when the project goes back to Thulu", () => {
    expect(placeBody("", "/Users/a/code/x")).toEqual({ runner_id: "", runner_path: "" });
  });
  it("trims the folder for a machine", () => {
    expect(placeBody("r1", "  /Users/a/code/x ")).toEqual({
      runner_id: "r1",
      runner_path: "/Users/a/code/x",
    });
  });
});

describe("codeOnlyOnRunner", () => {
  it("is true only for projects created from a folder on a machine", () => {
    expect(codeOnlyOnRunner({ id: "p", name: "p", source: "runner" })).toBe(true);
    expect(codeOnlyOnRunner({ id: "p", name: "p", runner_id: "r1" })).toBe(false);
  });
});
