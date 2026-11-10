// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { restore, scrolledIn } from "./persist-scroll";

describe("persist-scroll", () => {
  it("records only scrolled elements and puts them back after a move", () => {
    const root = document.createElement("aside");
    root.innerHTML = `<div class="list"></div><div class="still"></div>`;
    document.body.append(root);
    const list = root.querySelector<HTMLElement>(".list");
    if (!list) throw new Error("no list");
    Object.defineProperty(list, "scrollTop", { value: 0, writable: true });
    list.scrollTop = 840;

    const saved = scrolledIn([root]);
    expect(saved.map((s) => s.el)).toEqual([list]);

    list.scrollTop = 0;
    restore(saved);
    expect(list.scrollTop).toBe(840);
  });
});
