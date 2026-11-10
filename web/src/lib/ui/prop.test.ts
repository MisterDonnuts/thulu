// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import {
  propEmptyHTML,
  propListClasses,
  propRowHTML,
  propValueA11y,
  setPropListLayout,
} from "./prop";

function q(root: ParentNode, sel: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(sel);
  if (!found) throw new Error(`no ${sel}`);
  return found;
}

function el(html: string): HTMLElement {
  const box = document.createElement("div");
  box.innerHTML = html;
  return box.firstElementChild as HTMLElement;
}

describe("propListClasses", () => {
  it("is rows by default and auto on demand, with the extras last", () => {
    expect(propListClasses()).toBe("prop-list");
    expect(propListClasses({ layout: "auto", cls: "props" })).toBe(
      "prop-list prop-list-auto props",
    );
    expect(propListClasses({ layout: "sheet" })).toBe("prop-list prop-list-sheet");
  });

  it("throws on a layout that does not exist", () => {
    // @ts-expect-error a layout outside the type
    expect(() => propListClasses({ layout: "grid" })).toThrow(/no layout "grid"/);
  });
});

describe("setPropListLayout", () => {
  it("swaps the layout and keeps the page's classes", () => {
    const list = el(
      `<div class="${propListClasses({ layout: "auto", cls: "props show-all" })}"></div>`,
    );
    setPropListLayout(list, "rows");
    expect(list.className).toBe("prop-list props show-all");
    setPropListLayout(list, "sheet");
    expect(list.className).toBe("prop-list props show-all prop-list-sheet");
    setPropListLayout(list, "auto");
    expect(list.classList.contains("prop-list-auto")).toBe(true);
    expect(list.classList.contains("prop-list-sheet")).toBe(false);
    expect(list.classList.contains("show-all")).toBe(true);
  });
});

describe("propRowHTML", () => {
  it("makes an editable value the button that opens the picker", () => {
    const row = el(
      propRowHTML({
        name: "stage",
        label: "Stage",
        icon: "i-columns",
        valueHTML: '<span class="tag">Backlog</span>',
        edit: "Change the stage",
        valueAttrs: { "data-edit": "stage" },
        cls: "props-wide",
      }),
    );
    expect(row.className).toBe("nprop props-wide");
    expect(row.dataset.prop).toBe("stage");
    expect(row.querySelector(".nprop-label svg")?.getAttribute("aria-hidden")).toBe("true");
    const value = q(row, ".nprop-value");
    expect(value.className).toBe("nprop-value");
    expect(value.getAttribute("role")).toBe("button");
    expect(value.tabIndex).toBe(0);
    expect(value.getAttribute("aria-haspopup")).toBe("true");
    expect(value.getAttribute("aria-label")).toBe("Change the stage");
    expect(value.dataset.edit).toBe("stage");
    expect(value.innerHTML).toBe('<span class="tag">Backlog</span>');
  });

  it("leaves a read-only value out of the tab order, with its own edit button after it", () => {
    const row = el(
      propRowHTML({
        name: "projects",
        label: "Projects <&>",
        valueHTML: "x",
        trailingHTML: '<button type="button">e</button>',
      }),
    );
    const value = q(row, ".nprop-value");
    expect(value.className).toBe("nprop-value nprop-static");
    expect(value.hasAttribute("role")).toBe(false);
    expect(value.hasAttribute("tabindex")).toBe(false);
    expect(value.querySelector("button")?.textContent).toBe("e");
    expect(row.querySelector(".nprop-label")?.textContent).toBe("Projects <&>");
  });

  it("drops the trailing button of an editable value: the value is the button", () => {
    const html = propRowHTML({
      name: "x",
      label: "X",
      valueHTML: "v",
      edit: "Edit",
      trailingHTML: "<button>no</button>",
    });
    expect(html).not.toContain("<button>no</button>");
    expect(propValueA11y(undefined)).toEqual({});
  });
});

describe("propEmptyHTML", () => {
  it("escapes the quiet word", () => {
    expect(propEmptyHTML("<none>")).toBe('<span class="nprop-value-empty">&lt;none&gt;</span>');
  });
});
