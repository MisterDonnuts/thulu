// @vitest-environment jsdom
// What page-head.css does to a one-line trail (crumbsHTML with line: true),
// read in jsdom against the markup lib/ui/page-head.ts writes. It lives here
// for the same reason as picker-sheet.test.mjs: it reads the sheet off the
// disk. jsdom lays nothing out, so it checks the rules that keep the trail in
// its row (which steps shrink, the floor, the clip); the widths themselves
// are left to the browser.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import { crumbsHTML } from "../src/lib/ui/page-head";

const sheet = readFileSync(
  join(
    dirname(fileURLToPath(import.meta.url)),
    "..",
    "src",
    "styles",
    "primitives",
    "page-head.css",
  ),
  "utf8",
);

beforeAll(() => {
  const style = document.createElement("style");
  style.textContent = sheet;
  document.head.appendChild(style);
});

afterEach(() => {
  document.body.innerHTML = "";
});

function trail(line) {
  document.body.innerHTML = crumbsHTML(
    [
      { label: "A board with a very long name", href: "/tasks/?board=b" },
      { label: "The parent task", href: "#task-p" },
      { label: "a1b2c3d4", mono: true },
    ],
    { size: "sm", line, actionHTML: '<button type="button">Copy</button>' },
  );
  const steps = [...document.querySelectorAll(".crumb")];
  return {
    list: document.querySelector(".crumbs-list"),
    steps,
    action: document.querySelector(".crumbs-action"),
  };
}

const css = (el, prop) => getComputedStyle(el).getPropertyValue(prop);

describe("page-head.css, a one-line trail", () => {
  it("lets every step before the last shrink, down to a floor, and keeps the last whole", () => {
    const { steps } = trail(true);
    for (const step of steps.slice(0, -1)) {
      expect(css(step, "flex-shrink")).toBe("1");
      expect(css(step, "min-width")).toBe("4em");
      expect(css(step, "white-space")).toBe("nowrap");
    }
    const last = steps.at(-1);
    expect(css(last, "flex-shrink")).toBe("0");
  });

  it("clips the list so nothing paints under the action, which keeps its size", () => {
    const { list, action } = trail(true);
    expect(css(list, "overflow")).toBe("hidden");
    expect(css(list, "flex-wrap")).toBe("nowrap");
    expect(css(action, "flex-shrink")).toBe("0");
  });

  it("leaves a trail that wraps alone", () => {
    const { list, steps } = trail(false);
    expect(css(list, "overflow")).not.toBe("hidden");
    expect(css(list, "flex-wrap")).toBe("wrap");
    expect(css(steps[0], "min-width")).toBe("0px");
  });
});
