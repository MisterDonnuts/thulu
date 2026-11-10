// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { t } from "../i18n";
import { countHTML } from "./ui";
import { paintUpdates, updateBadgeHTML, UPDATES_COUNT, workshopButtonLabel } from "./workshop";

describe("updateBadgeHTML", () => {
  it("is an accent badge whose tooltip says why", () => {
    const html = updateBadgeHTML();
    expect(html).toContain('class="badge badge-accent"');
    expect(html).toContain(t("workshop.update_badge"));
    expect(html).toContain(`title="${t("workshop.update_badge_hint")}"`);
  });
});

describe("paintUpdates", () => {
  function setup() {
    document.body.innerHTML = `<button id="b">${countHTML(0, { ...UPDATES_COUNT, id: "c" })}</button>`;
    return {
      button: document.getElementById("b") as HTMLElement,
      count: document.getElementById("c") as HTMLElement,
    };
  }

  it("starts hidden, as Count draws it with nothing waiting", () => {
    expect(setup().count.hidden).toBe(true);
  });

  it("shows the number in an accent pill and names it on the button", () => {
    const { button, count } = setup();
    paintUpdates(button, count, 3);
    expect(count.hidden).toBe(false);
    expect(count.className).toBe("count-pill count-accent");
    expect(count.textContent).toBe("3");
    expect(button.getAttribute("aria-label")).toBe(workshopButtonLabel(3));
    expect(workshopButtonLabel(3)).toContain(t("workshop.open"));
  });

  it("hides again when nothing waits", () => {
    const { button, count } = setup();
    paintUpdates(button, count, 2);
    paintUpdates(button, count, 0);
    expect(count.hidden).toBe(true);
    expect(button.getAttribute("aria-label")).toBe(t("workshop.open"));
  });
});
