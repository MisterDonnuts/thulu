import { describe, expect, it } from "vitest";

import { WIKI_BASE, wikiUrl } from "./wiki";

const BASE = "https://wiki.example.test";

describe("wikiUrl", () => {
  it("joins the base, the page and the anchor", () => {
    expect(wikiUrl("/configure/mcps/", "register-your-own-mcp", BASE)).toBe(
      `${BASE}/configure/mcps/#register-your-own-mcp`,
    );
  });

  it("adds the leading slash a bare path lacks", () => {
    expect(wikiUrl("configure/skills/", undefined, BASE)).toBe(`${BASE}/configure/skills/`);
  });

  it("drops a trailing slash on the base", () => {
    expect(wikiUrl("/configurar/", undefined, `${BASE}/`)).toBe(`${BASE}/configurar/`);
  });

  it("builds no link while the wiki has no address", () => {
    expect(wikiUrl("/configure/mcps/", "register-your-own-mcp", "")).toBe("");
  });

  it("uses WIKI_BASE by default", () => {
    expect(wikiUrl("/configure/mcps/")).toBe(wikiUrl("/configure/mcps/", undefined, WIKI_BASE));
  });
});
