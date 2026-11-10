import { describe, expect, it } from "vitest";

import { sourceLink } from "./source";

describe("sourceLink", () => {
  const repo = "https://github.com/MisterDonnuts/thulu";

  it("links a release to its tag", () => {
    expect(sourceLink(repo, "0.3.4")).toBe(`${repo}/tree/v0.3.4`);
  });

  it("links a dev build or an unknown version to the repository", () => {
    expect(sourceLink(repo + "/", "0.3.4-dev.gabc1234")).toBe(repo);
    expect(sourceLink(repo, "")).toBe(repo);
  });

  it("leaves a fork's own source URL alone", () => {
    expect(sourceLink("https://git.acme.io/thulu", "0.3.4")).toBe("https://git.acme.io/thulu");
  });
});
