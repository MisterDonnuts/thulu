// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  hasTaskCardFlag,
  setTaskCardFlag,
  taskCardClasses,
  taskCardEl,
  taskCardFlagClass,
  taskCardHTML,
  taskCardKidsId,
  taskCardShellHTML,
} from "./task-card";

describe("taskCardClasses", () => {
  it("writes task-card, then its states, then the extras", () => {
    expect(taskCardClasses()).toBe("task-card");
    expect(taskCardClasses(["dragging", "dim"], "x")).toBe(
      "task-card task-card-dragging task-card-dim x",
    );
  });

  it("refuses a state that does not exist", () => {
    // @ts-expect-error: not a flag
    expect(() => taskCardFlagClass("open")).toThrow(/no flag "open"/);
  });
});

describe("taskCardShellHTML", () => {
  it("has the tools, every slot, and a chevron tied to the rows", () => {
    const html = taskCardShellHTML("a1b2c3d4");
    expect(html).toContain('class="task-card-handle"');
    expect(html).toContain("task-card-menu-btn");
    expect(html).toContain('data-card-menu="" aria-haspopup="menu" aria-expanded="false"');
    expect(html).toContain('aria-label="Actions (.)"');
    for (const slot of ["part", "type", "title", "prio", "state", "meta", "progress", "foot-rest"])
      expect(html).toContain(`class="task-card-${slot}"`);
    expect(html).toContain('aria-controls="task-card-kids-a1b2c3d4"');
    expect(html).toContain('id="task-card-kids-a1b2c3d4"');
  });

  it("escapes the id it keys the rows by", () => {
    expect(taskCardKidsId("<x>")).toBe("task-card-kids-<x>");
    expect(taskCardShellHTML("<x>")).toContain("task-card-kids-&lt;x&gt;");
  });

  it("keeps the slots that start empty hidden", () => {
    const html = taskCardShellHTML("a");
    for (const slot of ["part", "state", "meta", "kids"])
      expect(html).toMatch(new RegExp(`class="task-card-${slot}"[^>]* hidden`));
    expect(html).toMatch(/class="task-card-expand"[^>]* hidden/);
  });
});

describe("taskCardHTML and taskCardEl", () => {
  it("writes a focusable card keyed by the task", () => {
    const html = taskCardHTML({ id: "t1", attrs: { "data-group": "g" } });
    expect(
      html.startsWith('<div class="task-card" data-id="t1" tabindex="0" data-group="g">'),
    ).toBe(true);
  });

  it("parses into an element whose states toggle", () => {
    const el = taskCardEl({ id: "t1", flags: ["lifted"] });
    expect(el.classList.contains("task-card")).toBe(true);
    expect(el.dataset.id).toBe("t1");
    expect(el.tabIndex).toBe(0);
    expect(hasTaskCardFlag(el, "lifted")).toBe(true);
    setTaskCardFlag(el, "lifted", false);
    setTaskCardFlag(el, "hl", true);
    expect(el.className).toBe("task-card task-card-hl");
    expect(el.querySelector(".task-card-title")).not.toBeNull();
  });
});
