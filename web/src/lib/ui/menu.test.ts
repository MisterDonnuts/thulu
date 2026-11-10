// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { ALT_KEY } from "../shortcuts";

import {
  bindMenu,
  dismissLayer,
  floatAt,
  menuClasses,
  menuFocusables,
  menuHTML,
  menuItemHTML,
  menuItemsHTML,
  menuKeyOf,
  menuPosition,
  type MenuEntry,
} from "./menu";

function byId<T extends HTMLElement = HTMLElement>(id: string): T {
  const el = document.getElementById(id);
  if (!el) throw new Error(`no #${id}`);
  return el as T;
}

describe("menuClasses", () => {
  it("adds the variants after the base class", () => {
    expect(menuClasses()).toBe("menu");
    expect(menuClasses({ density: "rich", align: "start", floating: true, cls: "x" })).toBe(
      "menu menu-rich menu-start menu-floating x",
    );
  });

  it("refuses a density or an align it does not have", () => {
    // @ts-expect-error not a density
    expect(() => menuClasses({ density: "tight" })).toThrow(/density/);
    // @ts-expect-error not an align
    expect(() => menuClasses({ align: "center" })).toThrow(/align/);
  });
});

describe("menuItemHTML", () => {
  it("writes a compact action with its glyph and key cap", () => {
    expect(
      menuItemHTML({
        title: "Clone",
        icon: "copy",
        kbd: "C",
        key: "c",
        attrs: { "data-act": "clone" },
      }),
    ).toBe(
      '<button class="menu-item" type="button" role="menuitem" aria-keyshortcuts="C" tabindex="-1" data-key="c" data-act="clone">' +
        '<span class="menu-icon"><svg class="ico" aria-hidden="true"><use href="#i-copy"/></svg></span>' +
        '<span class="menu-body"><span class="menu-title">Clone</span></span>' +
        '<kbd class="kbd-sm menu-kbd" aria-hidden="true">C</kbd></button>',
    );
  });

  it("escapes every value", () => {
    const html = menuItemHTML({ title: "<b>", sub: '"x"', hint: "a&b" });
    expect(html).toContain("&lt;b&gt;");
    expect(html).toContain("&quot;x&quot;");
    expect(html).toContain("a&amp;b");
    expect(html).not.toContain("<b>");
  });

  it("keeps an off item focusable, says so and drops its link", () => {
    const html = menuItemHTML({
      title: "Delete",
      href: "/x",
      danger: true,
      disabled: true,
      sub: "Admins only",
    });
    expect(html).toMatch(
      /^<a class="menu-item menu-item-danger" role="menuitem" aria-disabled="true"/,
    );
    expect(html).not.toContain("href");
    expect(html).toContain('<span class="menu-sub">Admins only</span>');
  });

  it("makes a checked item a radio with a check, and an unchecked one a radio without", () => {
    const on = menuItemHTML({ title: "Review", dot: "#bb86fc", checked: true });
    expect(on).toContain('role="menuitemradio" aria-checked="true"');
    expect(on).toContain(
      '<span class="dot" aria-hidden="true" style="--dot-color:#bb86fc"></span>',
    );
    expect(on).toContain('class="ico menu-check"');
    const off = menuItemHTML({ title: "Done", checked: false, role: "menuitemcheckbox" });
    expect(off).toContain('role="menuitemcheckbox" aria-checked="false"');
    expect(off).not.toContain("menu-check");
  });

  it("refuses a checked plain item", () => {
    expect(() => menuItemHTML({ title: "x", checked: true, role: "menuitem" })).toThrow(/checked/);
  });

  it("drops a colour that is not a hex", () => {
    expect(menuItemHTML({ title: "x", dot: "red;background:url(x)" })).not.toContain("style");
  });

  it("reserves the lead with indent, and takes markup over a glyph", () => {
    expect(menuItemHTML({ title: "Move left", indent: true })).toContain(
      '<span class="menu-icon"></span>',
    );
    expect(menuItemHTML({ title: "High", icon: "flag", leadHTML: "<i>!</i>" })).toContain(
      '<span class="menu-icon"><i>!</i></span>',
    );
  });

  it("names a chord's keys for aria-keyshortcuts and keeps ids and keepOpen", () => {
    const html = menuItemHTML({
      title: "Left",
      kbd: [ALT_KEY, "←"],
      titleId: "t1",
      subId: "s1",
      sub: "x",
      keepOpen: true,
      hidden: true,
    });
    expect(html).toContain('aria-keyshortcuts="Alt+ArrowLeft"');
    expect(html).toContain('<span id="t1" class="menu-title">');
    expect(html).toContain('<span id="s1" class="menu-sub">');
    expect(html).toContain("data-keep-open");
    expect(html).toContain(" hidden");
  });
});

describe("menuItemsHTML", () => {
  it("writes heads, notes, rules and slots between the items", () => {
    const entries: MenuEntry[] = [
      { head: "a@b.c", sub: "admin" },
      { note: "You can only read this task." },
      { title: "Open" },
      { sep: true },
      { slot: "theme", html: '<div class="seg"></div>' },
    ];
    expect(menuItemsHTML(entries)).toBe(
      '<div class="menu-head"><span class="menu-head-body"><span class="menu-head-title">a@b.c</span><span class="menu-head-sub">admin</span></span></div>' +
        '<p class="menu-note" role="note">You can only read this task.</p>' +
        menuItemHTML({ title: "Open" }) +
        '<div class="menu-sep" role="separator"></div>' +
        '<div class="menu-slot"><div class="seg"></div></div>',
    );
  });
});

describe("menuHTML", () => {
  it("is a hidden menu with a name", () => {
    expect(menuHTML([{ title: "Open" }], { id: "m", labelledBy: "btn", density: "rich" })).toBe(
      `<div class="menu menu-rich" id="m" role="menu" aria-labelledby="btn" tabindex="-1" hidden>${menuItemHTML({ title: "Open" })}</div>`,
    );
  });

  it("refuses a menu without a name", () => {
    expect(() => menuHTML([], {})).toThrow(/label/);
  });

  it("shows an open one", () => {
    expect(menuHTML([], { label: "Actions", open: true })).not.toContain("hidden");
  });
});

describe("menuKeyOf", () => {
  it("reads letters in lowercase, Delete and Backspace as delete, and nothing else", () => {
    expect(menuKeyOf({ key: "C" })).toBe("c");
    expect(menuKeyOf({ key: "Backspace" })).toBe("delete");
    expect(menuKeyOf({ key: "Delete" })).toBe("delete");
    expect(menuKeyOf({ key: "ArrowDown" })).toBe("");
  });
});

describe("menuPosition", () => {
  const vp = { width: 1000, height: 800 };
  const menu = { width: 200, height: 300 };

  it("lines up with the anchor's end edge by default", () => {
    expect(menuPosition({ left: 500, top: 100, right: 528, bottom: 128 }, menu, vp)).toEqual({
      left: 328,
      top: 132,
    });
  });

  it("lines up with the start edge when asked, and flips to the end near the right", () => {
    const a = { left: 500, top: 100, right: 528, bottom: 128 };
    expect(menuPosition(a, menu, vp, "start")).toEqual({ left: 500, top: 132 });
    const nearRight = { left: 900, top: 100, right: 990, bottom: 128 };
    expect(menuPosition(nearRight, menu, vp, "start").left).toBe(790);
  });

  it("opens upward near the bottom and stays 8px inside", () => {
    expect(menuPosition({ left: 0, top: 700, right: 28, bottom: 728 }, menu, vp)).toEqual({
      left: 8,
      top: 396,
    });
  });
});

describe("floatAt", () => {
  it("takes off where a transformed ancestor puts fixed (0, 0)", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);
    el.getBoundingClientRect = () => new DOMRect(300, 50, 200, 100);
    Object.defineProperty(el, "offsetWidth", { value: 200 });
    Object.defineProperty(el, "offsetHeight", { value: 100 });
    floatAt(el, { left: 600, top: 100, right: 628, bottom: 128 }, "start");
    expect(el.style.getPropertyValue("--float-x")).toBe("300px");
    expect(el.style.getPropertyValue("--float-y")).toBe("82px");
    el.remove();
  });
});

function mount(entries: MenuEntry[], look: { floating?: boolean } = {}) {
  document.body.innerHTML = `<button id="btn" type="button">More</button>${menuHTML(entries, {
    id: "m",
    labelledBy: "btn",
    ...look,
  })}<button id="after" type="button">After</button>`;
  return {
    btn: document.getElementById("btn") as HTMLButtonElement,
    menu: document.getElementById("m") as HTMLElement,
  };
}

const press = (el: Element, key: string, init: KeyboardEventInit = {}) =>
  el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));

const entries: MenuEntry[] = [
  { title: "Open", key: "o", attrs: { "data-act": "open" } },
  { title: "Hidden", hidden: true, attrs: { "data-act": "hidden" } },
  { sep: true },
  { title: "Clone", key: "c", attrs: { "data-act": "clone" } },
  { title: "Delete", key: "delete", disabled: true, attrs: { "data-act": "delete" } },
];

describe("bindMenu", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("marks the trigger and toggles the menu on click, focusing the first item", () => {
    const { btn, menu } = mount(entries);
    const h = bindMenu(menu, btn);
    expect(btn.getAttribute("aria-haspopup")).toBe("menu");
    expect(btn.getAttribute("aria-controls")).toBe("m");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    btn.click();
    expect(h.isOpen()).toBe(true);
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("open");
    btn.click();
    expect(h.isOpen()).toBe(false);
  });

  it("walks the items in view with the arrows, off ones included, wrapping", () => {
    const { btn, menu } = mount(entries);
    bindMenu(menu, btn).open();
    expect(menuFocusables(menu).map((el) => el.dataset.act)).toEqual(["open", "clone", "delete"]);
    press(menu, "ArrowDown");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("clone");
    press(menu, "End");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("delete");
    press(menu, "ArrowDown");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("open");
    press(menu, "ArrowUp");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("delete");
  });

  it("opens from the trigger with ArrowUp on the last item", () => {
    const { btn, menu } = mount(entries);
    bindMenu(menu, btn);
    press(btn, "ArrowUp");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("delete");
  });

  it("runs an item, closing first and giving the focus back to the trigger", () => {
    const { btn, menu } = mount(entries);
    const seen: string[] = [];
    const h = bindMenu(menu, btn, {
      onSelect: (item) => seen.push(`${item.dataset.act}:${h.isOpen()}`),
    });
    h.open();
    (menu.querySelector('[data-act="clone"]') as HTMLElement).click();
    expect(seen).toEqual(["clone:false"]);
    expect(document.activeElement).toBe(btn);
  });

  it("runs an item by its letter, and never an off one", () => {
    const { btn, menu } = mount(entries);
    const onSelect = vi.fn();
    const h = bindMenu(menu, btn, { onSelect });
    h.open();
    press(document.activeElement as HTMLElement, "C");
    expect(onSelect).toHaveBeenCalledTimes(1);
    h.open();
    press(document.activeElement as HTMLElement, "Backspace");
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(h.isOpen()).toBe(true);
  });

  it("leaves a keepOpen item's menu open", () => {
    const { btn, menu } = mount([{ title: "Dark", checked: false, keepOpen: true }]);
    const onSelect = vi.fn();
    const h = bindMenu(menu, btn, { onSelect });
    h.open();
    (menu.querySelector(".menu-item") as HTMLElement).click();
    expect(onSelect).toHaveBeenCalled();
    expect(h.isOpen()).toBe(true);
  });

  it("closes on Escape before the page sees it, and focuses the trigger", () => {
    const { btn, menu } = mount(entries);
    const page = vi.fn();
    document.addEventListener("keydown", page);
    const h = bindMenu(menu, btn);
    h.open();
    press(document.activeElement as HTMLElement, "Escape");
    expect(h.isOpen()).toBe(false);
    expect(document.activeElement).toBe(btn);
    expect(page).not.toHaveBeenCalled();
    document.removeEventListener("keydown", page);
  });

  it("closes on a pointer down outside without moving the focus, not on the trigger", () => {
    const { btn, menu } = mount(entries);
    const h = bindMenu(menu, btn);
    h.open();
    btn.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(h.isOpen()).toBe(true);
    byId("after").dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(h.isOpen()).toBe(false);
    expect(document.activeElement).not.toBe(btn);
  });

  it("closes on Tab", () => {
    const { btn, menu } = mount(entries);
    const h = bindMenu(menu, btn);
    h.open();
    press(document.activeElement as HTMLElement, "Tab");
    expect(h.isOpen()).toBe(false);
  });

  it("keeps one menu open at a time", () => {
    document.body.innerHTML =
      `<button id="b1"></button>${menuHTML([{ title: "A" }], { id: "m1", labelledBy: "b1" })}` +
      `<button id="b2"></button>${menuHTML([{ title: "B" }], { id: "m2", labelledBy: "b2" })}`;
    const one = bindMenu(byId("m1"), byId("b1"));
    const two = bindMenu(byId("m2"), byId("b2"));
    one.open();
    two.open();
    expect(one.isOpen()).toBe(false);
    expect(two.isOpen()).toBe(true);
    two.close();
  });

  it("focuses the checked item when asked, and repaints before showing", () => {
    const { btn, menu } = mount([
      { title: "Light", checked: false },
      { title: "Dark", checked: true, id: "dark" },
    ]);
    const onOpen = vi.fn(() => expect(menu.hidden).toBe(true));
    bindMenu(menu, btn, { focus: "checked", onOpen }).open();
    expect(onOpen).toHaveBeenCalled();
    expect(document.activeElement?.id).toBe("dark");
  });

  it("places a floating menu at a point", () => {
    const { btn, menu } = mount(entries, { floating: true });
    const h = bindMenu(menu, btn);
    h.open({ x: 40, y: 60 });
    expect(menu.style.getPropertyValue("--float-x")).not.toBe("");
    expect(menu.style.getPropertyValue("--float-y")).not.toBe("");
    h.close();
  });

  it("stops once on a row of choices, at its checked one, and walks it with Left and Right", () => {
    const row = (["a", "b", "c"] as const)
      .map(
        (c) =>
          `<button type="button" role="menuitemradio" aria-checked="${c === "b"}" tabindex="-1" data-c="${c}" data-keep-open></button>`,
      )
      .join("");
    const { btn, menu } = mount([
      { title: "Rename", attrs: { "data-act": "rename" } },
      { slot: "colors", html: `<div role="group" aria-label="Colour" data-menu-row>${row}</div>` },
      { title: "Delete", attrs: { "data-act": "delete" } },
    ]);
    bindMenu(menu, btn).open();
    const at = () => document.activeElement as HTMLElement;
    press(menu, "ArrowDown");
    expect(at().dataset.c).toBe("b");
    press(menu, "ArrowRight");
    expect(at().dataset.c).toBe("c");
    press(menu, "ArrowRight");
    expect(at().dataset.c).toBe("a");
    press(menu, "ArrowLeft");
    expect(at().dataset.c).toBe("c");
    press(menu, "ArrowDown");
    expect(at().dataset.act).toBe("delete");
    press(menu, "ArrowUp");
    expect(at().dataset.c).toBe("b");
    press(menu, "ArrowUp");
    expect(at().dataset.act).toBe("rename");
    press(menu, "ArrowRight");
    expect(at().dataset.act).toBe("rename");
  });

  it("unbinds with its signal", () => {
    const { btn, menu } = mount(entries);
    const ctl = new AbortController();
    const h = bindMenu(menu, btn, { signal: ctl.signal });
    ctl.abort();
    btn.click();
    expect(h.isOpen()).toBe(false);
  });
});

describe("dismissLayer", () => {
  it("stops listening once released", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);
    const dismiss = vi.fn();
    const release = dismissLayer(el, null, dismiss);
    release();
    press(document.body, "Escape");
    expect(dismiss).not.toHaveBeenCalled();
    el.remove();
  });
});
