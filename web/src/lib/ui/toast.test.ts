// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { makeToast, toastClasses } from "./toast";

describe("toastClasses", () => {
  it("adds the kind except for info", () => {
    expect(toastClasses("info")).toBe("toast");
    expect(toastClasses("ok")).toBe("toast ok");
    expect(toastClasses("err")).toBe("toast err");
    expect(toastClasses("warn")).toBe("toast warn");
  });
});

describe("makeToast kinds", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = `<div id="toast-stack"></div>`;
  });
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  it("gives warn its class and glyph", () => {
    makeToast(10)("Saved, but the branch was ignored", "warn");
    const el = document.querySelector("#toast-stack .toast");
    expect(el?.className).toBe("toast warn");
    expect(el?.querySelector("use")?.getAttribute("href")).toBe("#i-warn");
    expect(el?.hasAttribute("role")).toBe(false);
  });

  it("makes a failure an alert and leaves the rest to the polite stack", () => {
    const toast = makeToast(10);
    toast("Could not save", "err");
    toast("Saved", "ok");
    toast("Heads up");
    const els = document.querySelectorAll("#toast-stack .toast");
    expect(els[0].getAttribute("role")).toBe("alert");
    expect(els[1].hasAttribute("role")).toBe(false);
    expect(els[2].className).toBe("toast");
    expect(els[2].querySelector("use")?.getAttribute("href")).toBe("#i-info");
  });

  it("closes itself after its time", () => {
    makeToast(10)("Saved", "ok");
    vi.advanceTimersByTime(10);
    vi.advanceTimersByTime(220);
    expect(document.querySelector("#toast-stack .toast")).toBeNull();
  });
});
