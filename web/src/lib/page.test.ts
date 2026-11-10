// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

function routerHashLink(hash: string): void {
  history.pushState({ index: 1, scrollX: 0, scrollY: 0 }, "", hash);
  window.dispatchEvent(new PopStateEvent("popstate", { state: null }));
}

describe("hash bridge", () => {
  it("reopens the hash a page dropped with replaceURL", async () => {
    // eslint-disable-next-line no-restricted-syntax -- sets the page up
    history.replaceState({ index: 0, scrollX: 0, scrollY: 0 }, "", "/evidence/#case=EV-2");
    const { replaceURL } = await import("./page");
    const seen: string[] = [];
    window.addEventListener("hashchange", () => seen.push(window.location.hash));

    replaceURL("/evidence/");
    routerHashLink("#case=EV-2");

    expect(seen).toEqual(["#case=EV-2"]);
  });

  it("stays quiet when the link points at the hash already showing", async () => {
    const { replaceURL } = await import("./page");
    replaceURL("/evidence/#case=EV-3");
    const seen: string[] = [];
    window.addEventListener("hashchange", () => seen.push(window.location.hash));

    routerHashLink("#case=EV-3");

    expect(seen).toEqual([]);
  });
});
