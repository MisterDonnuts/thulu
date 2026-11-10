// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { makeToast } from "./ui";

describe("makeToast", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("escapes the text and appends the link after it", () => {
    document.body.innerHTML = `<div id="toast-stack"></div>`;
    makeToast(10)("No <b>token</b>", "err", 10, { href: "/project/?id=p1#repo", label: "Open" });
    const el = document.querySelector("#toast-stack .toast");
    expect(el?.classList.contains("err")).toBe(true);
    expect(el?.querySelector("span")?.textContent).toBe("No <b>token</b> Open");
    const a = el?.querySelector<HTMLAnchorElement>("a.toast-link");
    expect(a?.getAttribute("href")).toBe("/project/?id=p1#repo");
  });

  it("carries no link unless given one", () => {
    document.body.innerHTML = `<div id="toast-stack"></div>`;
    makeToast(10)("Saved", "ok");
    expect(document.querySelector("#toast-stack a")).toBeNull();
  });
});
