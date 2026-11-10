// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { bindDrawer, bindDrawerResize, clampDrawerWidth } from "./drawer";
import { closeTopModal } from "./modal";
import { drawerHTML, type DrawerOptions } from "./ui/drawer";

afterEach(() => {
  document.body.innerHTML = "";
  localStorage.clear();
  vi.restoreAllMocks();
});

function mount(o: Partial<DrawerOptions> = {}): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = drawerHTML({ id: "d", title: "T", ...o });
  const el = tpl.content.firstElementChild as HTMLElement;
  document.body.appendChild(el);
  return el;
}

const nextFrame = () => new Promise((r) => requestAnimationFrame(() => r(undefined)));

describe("bindDrawer, modal", () => {
  it("drives a modal drawer as a modal: the box takes the focus, Escape closes it", async () => {
    const el = mount();
    const d = bindDrawer(el);
    d.open();
    expect(d.isOpen()).toBe(true);
    await nextFrame();
    expect(document.activeElement).toBe(el.querySelector(".drawer"));
    expect(closeTopModal(document.activeElement)).toBe(true);
    expect(d.isOpen()).toBe(false);
  });

  it("closes from the ✕ and from the backdrop", () => {
    const el = mount();
    const d = bindDrawer(el);
    d.open();
    (el.querySelector("[data-modal-close]") as HTMLElement).click();
    expect(d.isOpen()).toBe(false);
    d.open();
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(d.isOpen()).toBe(false);
  });
});

describe("bindDrawer, docked", () => {
  it("opens and closes the box, and gives the focus back to what opened it", async () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const box = mount({ modal: false });
    const d = bindDrawer(box);
    d.open();
    expect(box.classList.contains("open")).toBe(true);
    await nextFrame();
    expect(document.activeElement).toBe(box);
    d.close();
    expect(box.classList.contains("open")).toBe(false);
    expect(document.activeElement).toBe(opener);
  });

  it("focuses the field it is given", async () => {
    const box = mount({ modal: false, bodyHTML: '<input id="f">' });
    bindDrawer(box).open(box.querySelector<HTMLElement>("#f"));
    await nextFrame();
    expect(document.activeElement?.id).toBe("f");
  });

  it("closes from the ✕, asks beforeClose unless forced, and reports the close", () => {
    const box = mount({ modal: false });
    let allow = false;
    const onClose = vi.fn();
    const d = bindDrawer(box, { beforeClose: () => allow, onClose });
    d.open();
    (box.querySelector("[data-modal-close]") as HTMLElement).click();
    expect(d.isOpen()).toBe(true);
    d.close(true);
    expect(d.isOpen()).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    d.open();
    allow = true;
    d.close();
    expect(d.isOpen()).toBe(false);
  });

  it("is not a modal: Escape is the page's to decide", () => {
    const box = mount({ modal: false });
    const d = bindDrawer(box);
    d.open();
    expect(closeTopModal(null)).toBe(false);
    expect(d.isOpen()).toBe(true);
  });
});

describe("clampDrawerWidth", () => {
  it("keeps the width between the minimum and what leaves the page visible", () => {
    expect(clampDrawerWidth(500.4, 380, 160, 1440)).toBe(500);
    expect(clampDrawerWidth(100, 380, 160, 1440)).toBe(380);
    expect(clampDrawerWidth(2000, 380, 160, 1440)).toBe(1280);
    expect(clampDrawerWidth(Infinity, 380, 160, 1440)).toBe(1280);
  });

  it("gives the minimum on a window too narrow to keep anything", () => {
    expect(clampDrawerWidth(600, 380, 160, 390)).toBe(380);
  });
});

describe("bindDrawerResize", () => {
  function resizable(side: "right" | "left", width = 600): { box: HTMLElement; grip: HTMLElement } {
    const box = mount({ modal: false, resizable: true, side });
    vi.spyOn(box, "getBoundingClientRect").mockImplementation(() => {
      const w = parseFloat(box.style.width) || width;
      return { width: w } as DOMRect;
    });
    const grip = box.querySelector(".drawer-resizer") as HTMLElement;
    grip.setPointerCapture = () => undefined;
    grip.hasPointerCapture = () => false;
    return { box, grip };
  }
  const key = (grip: HTMLElement, k: string, shiftKey = false) =>
    grip.dispatchEvent(new KeyboardEvent("keydown", { key: k, shiftKey, bubbles: true }));

  it("reports the width on the separator", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const { box, grip } = resizable("right");
    bindDrawerResize(box, { min: 380, keep: 160 });
    expect(grip.getAttribute("aria-valuenow")).toBe("600");
    expect(grip.getAttribute("aria-valuemin")).toBe("380");
    expect(grip.getAttribute("aria-valuemax")).toBe("1280");
  });

  it("widens a right drawer with ArrowLeft and a left one with ArrowRight", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const right = resizable("right");
    bindDrawerResize(right.box);
    key(right.grip, "ArrowLeft");
    expect(right.box.style.width).toBe("616px");
    key(right.grip, "ArrowRight", true);
    expect(right.box.style.width).toBe("568px");

    const left = resizable("left");
    bindDrawerResize(left.box);
    key(left.grip, "ArrowRight");
    expect(left.box.style.width).toBe("616px");
    expect(left.grip.getAttribute("aria-valuenow")).toBe("616");
  });

  it("takes Home and End to the narrowest and the widest", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const { box, grip } = resizable("right");
    bindDrawerResize(box, { min: 400, keep: 200 });
    key(grip, "End");
    expect(box.style.width).toBe("1240px");
    key(grip, "Home");
    expect(box.style.width).toBe("400px");
  });

  it("remembers the width under its key and puts it back on the next page", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const first = resizable("right");
    bindDrawerResize(first.box, { storageKey: "test.w" });
    key(first.grip, "ArrowLeft");
    expect(localStorage.getItem("test.w")).toBe("616");

    document.body.innerHTML = "";
    const again = resizable("right");
    bindDrawerResize(again.box, { storageKey: "test.w" });
    expect(again.box.style.width).toBe("616px");
  });

  it("drags with the pointer and stores the width on release", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const { box, grip } = resizable("right");
    bindDrawerResize(box, { storageKey: "test.w" });
    const at = (type: string, clientX: number) =>
      grip.dispatchEvent(new MouseEvent(type, { clientX, bubbles: true }));
    at("pointerdown", 800);
    expect(box.dataset.resizing).toBe("true");
    at("pointermove", 700);
    expect(box.style.width).toBe("700px");
    at("pointerup", 700);
    expect(box.dataset.resizing).toBeUndefined();
    expect(localStorage.getItem("test.w")).toBe("700");
  });

  it("forgets the width on a double click", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    const { box, grip } = resizable("right");
    bindDrawerResize(box, { storageKey: "test.w" });
    key(grip, "ArrowLeft");
    grip.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(box.style.width).toBe("");
    expect(localStorage.getItem("test.w")).toBe(null);
  });

  it("does nothing on a drawer with no grab zone", () => {
    const box = mount({ modal: false });
    expect(() => bindDrawerResize(box).reset()).not.toThrow();
    expect(() => bindDrawerResize(null)).not.toThrow();
  });
});
