import { describe, expect, it } from "vitest";

import { newProjects, repoHint, repoMatches, shortPath, splitRepos } from "./project-runner-source";

const repo = {
  path: "/Users/a/code/shop",
  name: "shop",
  branch: "main",
  origin: "github.com/acme/shop",
};

describe("repoMatches", () => {
  it("matches everything on an empty filter", () => {
    expect(repoMatches(repo, "  ")).toBe(true);
  });
  it("matches name, path, branch or origin, ignoring case", () => {
    expect(repoMatches(repo, "SHOP")).toBe(true);
    expect(repoMatches(repo, "code/")).toBe(true);
    expect(repoMatches(repo, "acme")).toBe(true);
    expect(repoMatches(repo, "main")).toBe(true);
    expect(repoMatches(repo, "billing")).toBe(false);
  });
});

describe("shortPath", () => {
  it("writes a macOS or Linux home as ~", () => {
    expect(shortPath("/Users/ana/code/shop")).toBe("~/code/shop");
    expect(shortPath("/home/ana")).toBe("~");
    expect(shortPath("/Users/ana")).toBe("~");
  });
  it("leaves other paths alone", () => {
    expect(shortPath("/srv/code/shop")).toBe("/srv/code/shop");
    expect(shortPath("/Usersx/ana/code")).toBe("/Usersx/ana/code");
  });
});

describe("repoHint", () => {
  it("shows the short path and the branch the runner reported", () => {
    expect(repoHint({ path: "/x/y", name: "y" })).toBe("/x/y");
    expect(repoHint(repo)).toBe("~/code/shop · main");
  });
});

describe("splitRepos and newProjects", () => {
  const repos = [
    { path: "/c/a", name: "a" },
    { path: "/c/b", name: "b", project_id: "p1", project_name: "b" },
    { path: "/c/c", name: "c", project_id: "p2", project_name: "c" },
  ];
  it("separates the folders still free from the ones a project uses", () => {
    const { free, taken } = splitRepos(repos);
    expect(free.map((r) => r.name)).toEqual(["a"]);
    expect(taken.map((r) => r.name)).toEqual(["b", "c"]);
  });
  it("finds the projects that appeared since the list first loaded", () => {
    expect(newProjects(repos, new Set(["p1"])).map((r) => r.project_id)).toEqual(["p2"]);
    expect(newProjects(repos, new Set(["p1", "p2"]))).toEqual([]);
  });
});
