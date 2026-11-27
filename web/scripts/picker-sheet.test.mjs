// @vitest-environment jsdom
// What picker.css paints, read in jsdom against the markup lib/ui/picker.ts
// writes. It lives here, not next to picker.test.ts, because it reads the
// sheet off the disk (Vitest hands a CSS import over empty) and src/ has no
// Node types. The focused list (:focus-within) is left to the browser.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { bindPicker, openPicker, pickerHTML } from "../src/lib/ui/picker";

const sheet = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "src", "styles", "primitives", "picker.css"),
  "utf8",
);

// jsdom computes an unset background too, so the active option counts as
// marked when it paints differently from an option that is not active.
function marksActive(el) {
  const opt = el.querySelector(".nselect-item.active");
  const rest = el.querySelector(".nselect-item:not(.active)");
  if (!opt || !rest) throw new Error("needs an active option and another one");
  const bg = (o) => getComputedStyle(o).getPropertyValue("background");
  return bg(opt) !== bg(rest);
}

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn();
  const style = document.createElement("style");
  // The token sheet is not loaded; jsdom resolves var(), so give the one the
  // marked option paints with a value, or it computes to transparent.
  style.textContent = `:root { --surface-4: rgb(1, 2, 3); }\n${sheet}`;
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
