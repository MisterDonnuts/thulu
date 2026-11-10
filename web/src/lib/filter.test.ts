import { describe, expect, it } from "vitest";

import { matchesQuery } from "./filter";

describe("matchesQuery", () => {
  it("matches everything on an empty query", () => {
    expect(matchesQuery("", ["anything"])).toBe(true);
    expect(matchesQuery("", [])).toBe(true);
  });

  it("is case-insensitive on both sides", () => {
    expect(matchesQuery("OCTO", ["octopus"])).toBe(true);
    expect(matchesQuery("octa", ["Octavia"])).toBe(true);
  });

  it("matches when any one field contains the query", () => {
    expect(matchesQuery("git", ["octopus", "https://github.com/x", null])).toBe(true);
    expect(matchesQuery("zzz", ["octopus", "https://github.com/x", null])).toBe(false);
  });

  it("skips absent fields instead of matching them", () => {
    expect(matchesQuery("a", [undefined, null])).toBe(false);
  });
});
