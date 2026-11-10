// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { t } from "../../i18n";
import {
  chipA11y,
  chipClasses,
  chipDataAttr,
  chipHTML,
  chipPicked,
  filterBarClasses,
  filterBarHTML,
  filterChipsHTML,
  filterFieldClasses,
  filterFieldHTML,
  searchClasses,
  searchHTML,
  searchStyle,
  setChipsPressed,
} from "./toolbar";

function el(html: string): HTMLElement {
  const box = document.createElement("div");
  box.innerHTML = html;
  return box.firstElementChild as HTMLElement;
}

function pick<K extends keyof HTMLElementTagNameMap>(
  root: Element,
  sel: K,
): HTMLElementTagNameMap[K];
function pick(root: Element, sel: string): HTMLElement;
function pick(root: Element, sel: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(sel);
  if (!found) throw new Error(`no ${sel}`);
  return found;
}

describe("filterBarClasses", () => {
  it("writes filter-bar and the look, plain having no class of its own", () => {
    expect(filterBarClasses()).toBe("filter-bar");
    expect(filterBarClasses({ look: "boxed" })).toBe("filter-bar filter-bar-boxed");
    expect(filterBarClasses({ look: "ruled", cls: "board-toolbar" })).toBe(
      "filter-bar filter-bar-ruled board-toolbar",
    );
    expect(filterBarClasses({ look: "inset" })).toBe("filter-bar filter-bar-inset");
  });

  it("refuses a look that does not exist", () => {
    expect(() => filterBarClasses({ look: "card" as never })).toThrow(/no look "card"/);
  });
});

describe("filterBarHTML", () => {
  it("writes the filters as given and the end in its own wrapper", () => {
    expect(filterBarHTML({ start: "<b>f</b>", end: "<i>x</i>", look: "boxed", id: "bar" })).toBe(
      '<div id="bar" class="filter-bar filter-bar-boxed"><b>f</b><div class="filter-bar-end"><i>x</i></div></div>',
    );
  });

  it("names a div as a group and a section as itself", () => {
    const div = el(filterBarHTML({ start: "", label: "Filter <cases>" }));
    expect(div.tagName).toBe("DIV");
    expect(div.getAttribute("role")).toBe("group");
    expect(div.getAttribute("aria-label")).toBe("Filter <cases>");

    const section = el(filterBarHTML({ start: "", label: "Filters", as: "section" }));
    expect(section.tagName).toBe("SECTION");
    expect(section.hasAttribute("role")).toBe(false);
    expect(section.getAttribute("aria-label")).toBe("Filters");
  });

  it("writes no role on a bar without a name, nor an end without one", () => {
    expect(filterBarHTML({ start: "x", attrs: { hidden: true } })).toBe(
      '<div class="filter-bar" hidden>x</div>',
    );
  });

  it("refuses a tag that does not exist", () => {
    expect(() => filterBarHTML({ start: "", as: "nav" as never })).toThrow(/no tag "nav"/);
  });
});

describe("filterFieldHTML", () => {
  it("writes a field with the filter variant, its label tied to the control", () => {
    const html = filterFieldHTML({
      label: "Env & stage",
      for: "f-env",
      control: '<select id="f-env"></select>',
    });
    expect(html).toBe(
      '<div class="field field-filter"><label class="field-label" for="f-env">Env &amp; stage</label><select id="f-env"></select></div>',
    );
  });

  it("adds inline and grow, and hides the label for screen readers only", () => {
    expect(filterFieldClasses({ inline: true, grow: true, cls: "x" })).toBe(
      "field field-filter field-filter-inline field-filter-grow x",
    );
    const field = el(
      filterFieldHTML({
        label: "Kind",
        for: "k",
        hideLabel: true,
        control: "<select id=k></select>",
      }),
    );
    expect(field.querySelector("label")?.className).toBe("field-label visually-hidden");
  });
});

describe("searchHTML", () => {
  it("writes the box with its glyph, its input and the / cap", () => {
    const box = el(searchHTML({ id: "search", placeholder: "Find a <project>" }));
    expect(box.className).toBe("search");
    expect(box.hasAttribute("style")).toBe(false);
    const input = pick(box, "input");
    expect(input.id).toBe("search");
    expect(input.className).toBe("search-input");
    expect(input.hasAttribute("type")).toBe(false);
    expect(input.placeholder).toBe("Find a <project>");
    expect(input.getAttribute("aria-label")).toBe("Find a <project>");
    expect(input.getAttribute("aria-keyshortcuts")).toBe("/");
    expect(input.getAttribute("autocomplete")).toBe("off");
    expect(box.querySelector("svg.search-icon use")?.getAttribute("href")).toBe("#i-search");
    const cap = pick(box, "kbd");
    expect(cap.className).toBe("kbd-sm kbd-inset");
    expect(cap.getAttribute("aria-hidden")).toBe("true");
    expect(cap.textContent).toBe("/");
  });

  it("says Search when no placeholder is given, and takes a label over it", () => {
    const plain = pick(el(searchHTML({ id: "q" })), "input");
    expect(plain.placeholder).toBe(t("ui.labels.search"));
    const named = pick(el(searchHTML({ id: "q", label: "Search the cases" })), "input");
    expect(named.getAttribute("aria-label")).toBe("Search the cases");
  });

  it("drops the cap and its shortcut with kbd false, and passes the input's extras", () => {
    const box = el(
      searchHTML({
        id: "ws-q",
        kbd: false,
        type: "search",
        value: 'a"b',
        attrs: { spellcheck: "false", "data-x": 1 },
      }),
    );
    expect(box.querySelector("kbd")).toBeNull();
    const input = pick(box, "input");
    expect(input.type).toBe("search");
    expect(input.value).toBe('a"b');
    expect(input.hasAttribute("aria-keyshortcuts")).toBe(false);
    expect(input.getAttribute("spellcheck")).toBe("false");
    expect(input.dataset.x).toBe("1");
  });

  it("puts a width in --search-w and fluid in a class", () => {
    expect(searchStyle(360)).toBe("--search-w: 360px");
    expect(searchStyle()).toBeUndefined();
    expect(() => searchStyle(-1)).toThrow(/bad width/);
    expect(searchClasses({ fluid: true, cls: "grow" })).toBe("search search-fluid grow");
    const box = el(searchHTML({ id: "s", width: 280, fluid: true }));
    expect(box.getAttribute("style")).toBe("--search-w: 280px");
    expect(box.className).toBe("search search-fluid");
  });

  it("writes another key when asked", () => {
    const box = el(searchHTML({ id: "s", kbd: "F" }));
    expect(box.querySelector("kbd")?.textContent).toBe("F");
    expect(box.querySelector("input")?.getAttribute("aria-keyshortcuts")).toBe("F");
  });
});

describe("chipClasses and chipA11y", () => {
  it("writes chip, its size, menu and active", () => {
    expect(chipClasses()).toBe("chip");
    expect(chipClasses({ size: "md", menu: true, active: true, cls: "x" })).toBe(
      "chip chip-md chip-menu is-active x",
    );
    expect(() => chipClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
  });

  it("marks a toggle pressed and a menu chip as a popup trigger, never both", () => {
    expect(chipA11y({ pressed: false })).toEqual({ "aria-pressed": "false" });
    expect(chipA11y({})).toEqual({ "aria-pressed": undefined });
    expect(chipA11y({ menu: true, controls: "m" })).toEqual({
      "aria-haspopup": "menu",
      "aria-expanded": "false",
      "aria-controls": "m",
    });
    expect(chipA11y({ menu: true, expanded: true })["aria-expanded"]).toBe("true");
    expect(() => chipA11y({ menu: true, pressed: true })).toThrow(/not a toggle/);
  });
});

describe("chipHTML", () => {
  it("writes a plain button with the label in its span", () => {
    expect(chipHTML({ label: "24h & more", pressed: true, attrs: { "data-preset": "24h" } })).toBe(
      '<button class="chip" type="button" aria-pressed="true" data-preset="24h"><span class="chip-label">24h &amp; more</span></button>',
    );
  });

  it("puts the glyph, the dot, the count and the chevron in order", () => {
    const chip = el(
      chipHTML({
        label: "Blocked",
        icon: "lock",
        dot: "danger",
        count: 0,
        countId: "blocked-count",
        hideZero: true,
        size: "md",
        title: "Only blocked tasks",
        id: "blocked-btn",
        pressed: false,
      }),
    );
    expect(chip.id).toBe("blocked-btn");
    expect(chip.className).toBe("chip chip-md");
    expect(chip.title).toBe("Only blocked tasks");
    expect([...chip.children].map((c) => c.getAttribute("class"))).toEqual([
      "ico ico-sm",
      "dot dot-danger",
      "chip-label",
      "count chip-count",
    ]);
    const count = pick(chip, ".chip-count");
    expect(count.id).toBe("blocked-count");
    expect(count.hidden).toBe(true);
  });

  it("writes a menu chip with its chevron last and the label's id", () => {
    const chip = el(
      chipHTML({ label: "Type: all", labelId: "type-label", menu: true, controls: "type-menu" }),
    );
    expect(chip.getAttribute("aria-haspopup")).toBe("menu");
    expect(chip.hasAttribute("aria-pressed")).toBe(false);
    expect(chip.querySelector(".chip-label")?.id).toBe("type-label");
    expect(chip.lastElementChild?.getAttribute("class")).toBe("ico ico-sm chip-chevron");
  });

  it("disables a chip", () => {
    expect((el(chipHTML({ label: "x", disabled: true })) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("filterChipsHTML", () => {
  const items = [
    { value: "all", label: "All", count: 6 },
    { value: "failed", label: "Failed", count: 1, dot: "danger" as const },
    { value: "passed", label: "Passed", count: 2, dot: "ok" as const },
  ];

  it("writes a named group with one chip per value, pressed where picked", () => {
    const group = el(
      filterChipsHTML(items, "all", { attr: "status", size: "md", label: "By status" }),
    );
    expect(group.className).toBe("chip-group");
    expect(group.getAttribute("role")).toBe("group");
    expect(group.getAttribute("aria-label")).toBe("By status");
    const chips = [...group.querySelectorAll<HTMLElement>(".chip")];
    expect(chips.map((c) => c.dataset.status)).toEqual(["all", "failed", "passed"]);
    expect(chips.map((c) => c.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    expect(chips.every((c) => c.classList.contains("chip-md"))).toBe(true);
  });

  it("presses several values from a list, and none from null", () => {
    const many = el(filterChipsHTML(items, ["failed", "passed"]));
    expect([...many.querySelectorAll(".chip")].map((c) => c.getAttribute("aria-pressed"))).toEqual([
      "false",
      "true",
      "true",
    ]);
    expect(el(filterChipsHTML(items, null)).querySelectorAll('[aria-pressed="true"]')).toHaveLength(
      0,
    );
    expect(
      el(filterChipsHTML(items, null)).querySelector(".chip")?.hasAttribute("data-value"),
    ).toBe(true);
  });

  it("refuses a data attribute name that is not one", () => {
    expect(chipDataAttr("env")).toBe("data-env");
    expect(() => chipDataAttr('x" onclick="y')).toThrow(/bad data attribute/);
    expect(() => filterChipsHTML(items, "all", { attr: "A" })).toThrow(/bad data attribute/);
  });

  it("knows which values are picked", () => {
    expect(chipPicked("a", "a")).toBe(true);
    expect(chipPicked(["a", "b"], "b")).toBe(true);
    expect(chipPicked(undefined, "a")).toBe(false);
  });
});

describe("setChipsPressed", () => {
  it("moves the pressed state to the picked values", () => {
    const group = el(
      filterChipsHTML(
        [
          { value: "24h", label: "24h" },
          { value: "7d", label: "7d" },
        ],
        "24h",
        { attr: "preset" },
      ),
    );
    setChipsPressed(group, "7d", "preset");
    expect([...group.querySelectorAll(".chip")].map((c) => c.getAttribute("aria-pressed"))).toEqual(
      ["false", "true"],
    );
  });
});
