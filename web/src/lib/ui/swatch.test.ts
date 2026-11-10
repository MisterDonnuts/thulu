// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";

import {
  checkSwatch,
  moveSwatchFocus,
  swatchAttrs,
  swatchClasses,
  swatchPickerAttrs,
  swatchPickerClasses,
  swatchPickerHTML,
  swatchTabStop,
  type SwatchItem,
} from "./swatch";

const colours: SwatchItem[] = [
  { value: "#111111", label: "Ink" },
  { value: "#222222", label: "Slate <dark>", attrs: { "data-swatch": "#222222" } },
  { value: "#333333", label: "Stone", color: "var(--fg)" },
];

function mount(html: string): HTMLElement {
  document.body.innerHTML = html;
  return document.body.firstElementChild as HTMLElement;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("swatchPickerClasses and swatchClasses", () => {
  it("writes the picker, its kind, the box, then the extras", () => {
    expect(swatchPickerClasses()).toBe("swatch-picker swatch-picker-color");
    expect(swatchPickerClasses({ kind: "glyph", boxed: true, cls: "x" })).toBe(
      "swatch-picker swatch-picker-glyph swatch-picker-boxed x",
    );
    expect(swatchClasses("glyph")).toBe("swatch swatch-glyph");
  });

  it("throws on a kind or a role that does not exist", () => {
    expect(() => swatchPickerClasses({ kind: "pattern" as never })).toThrow(/no kind/);
    expect(() => swatchClasses("pattern" as never)).toThrow(/no kind/);
    expect(() => swatchPickerAttrs("option" as never)).toThrow(/no role/);
  });
});

describe("swatchPickerAttrs and swatchTabStop", () => {
  it("names a radiogroup, and marks a menu row for the Menu to walk", () => {
    expect(swatchPickerAttrs("radio", "Colour")).toEqual({
      role: "radiogroup",
      "aria-label": "Colour",
      "data-menu-row": undefined,
    });
    expect(swatchPickerAttrs("menuitemradio", "Colour")["data-menu-row"]).toBe("");
    expect(swatchPickerAttrs("menuitemradio", "Colour").role).toBe("group");
  });

  it("gives the tab stop to the picked swatch, else the first", () => {
    expect(swatchTabStop(colours, "#333333")).toBe(2);
    expect(swatchTabStop(colours, "#999999")).toBe(0);
    expect(swatchTabStop(colours, undefined)).toBe(0);
  });

  it("keeps a menu open only from a menu swatch", () => {
    const item = colours[0];
    expect(
      swatchAttrs(item, { role: "radio", checked: false, tabStop: true, keepOpen: true })[
        "data-keep-open"
      ],
    ).toBeUndefined();
    const inMenu = swatchAttrs(item, {
      role: "menuitemradio",
      checked: false,
      tabStop: true,
      keepOpen: true,
    });
    expect(inMenu["data-keep-open"]).toBe("");
    expect(inMenu.tabindex).toBe("-1");
  });
});

describe("swatchPickerHTML", () => {
  it("writes a radio per colour, named and filled, the picked one checked and tabbable", () => {
    const el = mount(
      swatchPickerHTML({ label: "Colour of Bug", items: colours, selected: "#222222" }),
    );
    expect(el.getAttribute("role")).toBe("radiogroup");
    expect(el.getAttribute("aria-label")).toBe("Colour of Bug");
    expect(el.hasAttribute("data-menu-row")).toBe(false);
    const sw = Array.from(el.querySelectorAll<HTMLButtonElement>("button.swatch.swatch-color"));
    expect(sw.map((s) => s.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(sw.map((s) => s.tabIndex)).toEqual([-1, 0, -1]);
    expect(sw[1].getAttribute("aria-label")).toBe("Slate <dark>");
    expect(sw[1].title).toBe("Slate <dark>");
    expect(sw[1].dataset.value).toBe("#222222");
    expect(sw[1].dataset.swatch).toBe("#222222");
    expect(sw[0].getAttribute("style")).toBe("--sw:#111111");
    expect(sw[2].getAttribute("style")).toBe("--sw:var(--fg)");
    expect(sw.every((s) => s.type === "button" && s.getAttribute("role") === "radio")).toBe(true);
  });

  it("writes a glyph per icon, with no fill", () => {
    const el = mount(
      swatchPickerHTML({
        kind: "glyph",
        boxed: true,
        label: "Icon",
        items: [{ value: "bug", label: "Bug", icon: "tt-bug" }],
      }),
    );
    const sw = el.querySelector<HTMLElement>(".swatch-glyph");
    expect(sw?.querySelector("use")?.getAttribute("href")).toBe("#i-tt-bug");
    expect(sw?.hasAttribute("style")).toBe(false);
    expect(sw?.tabIndex).toBe(0);
  });
});

describe("checkSwatch", () => {
  it("moves the check and the tab stop of a radio picker to the pick", () => {
    const el = mount(swatchPickerHTML({ label: "C", items: colours, selected: "#111111" }));
    const sw = Array.from(el.querySelectorAll<HTMLElement>(".swatch"));
    checkSwatch(sw[2]);
    expect(sw.map((s) => s.getAttribute("aria-checked"))).toEqual(["false", "false", "true"]);
    expect(sw.map((s) => s.tabIndex)).toEqual([-1, -1, 0]);
  });

  it("leaves the menu's focus order alone in a menu row", () => {
    const el = mount(
      swatchPickerHTML({ label: "C", items: colours, selected: "#111111", role: "menuitemradio" }),
    );
    const sw = Array.from(el.querySelectorAll<HTMLElement>(".swatch"));
    checkSwatch(sw[1]);
    expect(sw[1].getAttribute("aria-checked")).toBe("true");
    expect(sw.map((s) => s.tabIndex)).toEqual([-1, -1, -1]);
  });
});

describe("moveSwatchFocus", () => {
  const ten: SwatchItem[] = Array.from({ length: 10 }, (_, i) => ({
    value: String(i),
    label: `c${i}`,
  }));

  function press(target: HTMLElement, key: string): { handled: boolean; ev: KeyboardEvent } {
    const ev = new KeyboardEvent("keydown", { key, cancelable: true, bubbles: true });
    Object.defineProperty(ev, "target", { value: target });
    return { handled: moveSwatchFocus(ev), ev };
  }

  function layout(el: HTMLElement, cols: number): HTMLElement[] {
    const sw = Array.from(el.querySelectorAll<HTMLElement>(".swatch"));
    sw.forEach((s, i) =>
      Object.defineProperty(s, "offsetTop", { value: Math.floor(i / cols) * 32 }),
    );
    return sw;
  }

  it("steps one swatch with Left and Right and one row with Up and Down, wrapping", () => {
    const el = mount(swatchPickerHTML({ label: "C", items: ten, selected: "0" }));
    const sw = layout(el, 5);
    sw[0].focus();
    expect(press(sw[0], "ArrowRight").handled).toBe(true);
    expect(document.activeElement).toBe(sw[1]);
    expect(sw[1].tabIndex).toBe(0);
    expect(sw[0].tabIndex).toBe(-1);
    press(sw[1], "ArrowDown");
    expect(document.activeElement).toBe(sw[6]);
    press(sw[6], "ArrowUp");
    expect(document.activeElement).toBe(sw[1]);
    press(sw[0], "ArrowLeft");
    expect(document.activeElement).toBe(sw[9]);
    press(sw[3], "Home");
    expect(document.activeElement).toBe(sw[0]);
    press(sw[0], "End");
    expect(document.activeElement).toBe(sw[9]);
  });

  it("steps by one on Up and Down when the swatches share one row", () => {
    const el = mount(swatchPickerHTML({ label: "C", items: ten.slice(0, 3) }));
    const sw = layout(el, 3);
    press(sw[0], "ArrowDown");
    expect(document.activeElement).toBe(sw[1]);
  });

  it("prevents the page's scroll for its keys and leaves the rest", () => {
    const el = mount(swatchPickerHTML({ label: "C", items: ten }));
    const sw = layout(el, 5);
    expect(press(sw[0], "ArrowRight").ev.defaultPrevented).toBe(true);
    const enter = press(sw[0], "Enter");
    expect(enter.handled).toBe(false);
    expect(enter.ev.defaultPrevented).toBe(false);
  });

  it("is not the picker's business inside a menu row or outside a picker", () => {
    const row = mount(swatchPickerHTML({ label: "C", items: ten, role: "menuitemradio" }));
    const sw = row.querySelector<HTMLElement>(".swatch") as HTMLElement;
    expect(press(sw, "ArrowRight").handled).toBe(false);
    const loose = mount("<button>x</button>");
    expect(press(loose, "ArrowRight").handled).toBe(false);
  });
});
