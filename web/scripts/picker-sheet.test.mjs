// @vitest-environment jsdom
// What picker.css paints, read in jsdom against the markup lib/ui/picker.ts
// writes. It lives here, not next to picker.test.ts, because it reads the
// sheet off the disk (Vitest hands a CSS import over empty) and src/ has no
// Node types. jsdom resolves no var() and matches no :focus-within, so it
// reads the declared background without the focus; the focused list is
// left to the browser.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { bindPicker, openPicker, pickerHTML } from "../src/lib/ui/picker";

const sheet = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "src", "styles", "primitives", "picker.css"),
  "utf8",
);

function marksActive(el) {
  const opt = el.querySelector(".nselect-item.active");
  if (!opt) throw new Error("no active option");
  return getComputedStyle(opt).getPropertyValue("background") !== "";
}

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn();
  const style = document.createElement("style");
  style.textContent = sheet;
  document.head.appendChild(style);
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("picker.css", () => {
  it("leaves an inline picker's keyboard option unmarked while the focus is elsewhere", () => {
    document.body.innerHTML = pickerHTML({
      id: "ip",
      label: "Repositories",
      place: "inline",
      multi: true,
      items: [],
    });
    const el = document.getElementById("ip");
    const h = bindPicker(el, {
      items: ["infra", "web"].map((v) => ({ value: v, label: v, selected: v === "web" })),
      onSelect: () => undefined,
    });
    expect(el.querySelector(".nselect-item.active")?.getAttribute("data-value")).toBe("infra");
    expect(marksActive(el)).toBe(false);
    h.close();
  });

  it("marks where Enter lands in a floating picker that leaves the focus in a field", () => {
    document.body.innerHTML = `<textarea id="ta"></textarea>`;
    const ta = document.getElementById("ta");
    ta.focus();
    const h = openPicker(ta, {
      label: "Mention",
      focus: false,
      items: [
        { value: "a", label: "a" },
        { value: "b", label: "b" },
      ],
      onSelect: () => undefined,
    });
    expect(marksActive(h.el)).toBe(true);
    h.close();
  });
});
