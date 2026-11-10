import { describe, expect, it } from "vitest";
import { webRepoURL } from "./repo-url";

describe("webRepoURL", () => {
  it("strips the .git from an https clone URL", () => {
    expect(webRepoURL("https://github.com/acme/web.git")).toBe("https://github.com/acme/web");
  });
  it("leaves an https URL that has no .git alone", () => {
    expect(webRepoURL("https://github.com/acme/web")).toBe("https://github.com/acme/web");
  });
  it("rewrites scp-style SSH", () => {
    expect(webRepoURL("git@github.com:acme/web.git")).toBe("https://github.com/acme/web");
    expect(webRepoURL("git@gitlab.example.com:group/sub/web.git")).toBe(
      "https://gitlab.example.com/group/sub/web",
    );
  });
  it("gives nothing for a form it cannot read, so the button stays hidden", () => {
    expect(webRepoURL("")).toBe("");
    expect(webRepoURL("ssh://git@github.com/acme/web.git")).toBe("");
    expect(webRepoURL("/local/path/repo")).toBe("");
  });
});
