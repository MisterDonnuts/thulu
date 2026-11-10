// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { t } from "../../i18n";
import {
  stepFinishHTML,
  stepItemClasses,
  stepItemHTML,
  stepListClasses,
  stepListHTML,
  stepMark,
} from "./steps";

function parse(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

describe("stepMark", () => {
  it("gives each state a glyph and a word", () => {
    expect(stepMark("done")).toEqual({ icon: "check", label: t("ui.steps.done") });
    expect(stepMark("pending")).toEqual({ icon: "circle", label: t("ui.steps.pending") });
    expect(stepMark("blocked")).toEqual({ icon: "lock", label: t("ui.steps.blocked") });
    expect(stepMark("now")).toEqual({ icon: "circle-half", label: t("ui.steps.now") });
    expect(stepMark("unknown")).toEqual({ icon: "help", label: t("ui.steps.unknown") });
  });

  it("throws on a state that does not exist", () => {
    expect(() => stepMark("skipped" as never)).toThrow(/no state "skipped"/);
    expect(() => stepItemClasses("skipped" as never)).toThrow(/no state "skipped"/);
  });
});

describe("class lists", () => {
  it("writes the list and the step classes", () => {
    expect(stepListClasses()).toBe("step-list");
    expect(stepListClasses({ compact: true, cls: "fs" })).toBe("step-list step-list-compact fs");
    expect(stepItemClasses("done")).toBe("step-item step-item-done");
    expect(stepListClasses({ inline: true })).toBe("step-list step-list-inline");
  });

  it("refuses a list both compact and inline", () => {
    expect(() => stepListClasses({ compact: true, inline: true })).toThrow(/not both/);
  });
});

describe("the step under way", () => {
  it("is the current step, and only it", () => {
    const now = parse(stepItemHTML({ title: "Running", state: "now" }));
    expect(now.getAttribute("aria-current")).toBe("step");
    expect(now.className).toBe("step-item step-item-now");
    expect(now.querySelector("svg.step-mark")?.getAttribute("aria-label")).toBe(t("ui.steps.now"));
    const next = parse(stepItemHTML({ title: "Next", state: "pending" }));
    expect(next.hasAttribute("aria-current")).toBe(false);
  });
});

describe("stepItemHTML", () => {
  it("names the mark for screen readers instead of hiding it", () => {
    const el = parse(stepItemHTML({ title: "Add a project", state: "pending" }));
    const mark = el.querySelector("svg.step-mark");
    expect(mark?.getAttribute("role")).toBe("img");
    expect(mark?.getAttribute("aria-label")).toBe(t("ui.steps.pending"));
    expect(mark?.querySelector("use")?.getAttribute("href")).toBe("#i-circle");
    expect(el.dataset.state).toBe("pending");
  });

  it("links the title only to a safe place", () => {
    const linked = parse(stepItemHTML({ title: "Add", state: "pending", href: "/projects/" }));
    expect(linked.querySelector(".step-title a")?.getAttribute("href")).toBe("/projects/");
    const bad = parse(stepItemHTML({ title: "Add", state: "pending", href: "javascript:x" }));
    expect(bad.querySelector("a")).toBeNull();
    expect(bad.querySelector(".step-title")?.textContent).toBe("Add");
  });

  it("writes detail, note and finish in that order, and leaves out what is missing", () => {
    const el = parse(
      stepItemHTML({
        title: "Identity",
        state: "pending",
        detail: "0 providers",
        note: "Meanwhile: nobody signs in",
        finish: { text: "run", command: "thulu init" },
      }),
    );
    const lines = Array.from(el.querySelectorAll(".step-body > p"), (p) => p.className);
    expect(lines).toEqual(["step-title", "step-detail", "step-note", "step-finish"]);
    expect(parse(stepItemHTML({ title: "x", state: "done" })).querySelectorAll("p")).toHaveLength(
      1,
    );
  });

  it("escapes every text", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const el = parse(
      stepItemHTML({
        title: evil,
        state: "blocked",
        detail: evil,
        note: evil,
        finish: { text: evil, command: evil },
      }),
    );
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector(".step-title")?.textContent).toBe(evil);
  });
});

describe("a step's action", () => {
  it("is a link button after the title: a page, or a hook the page binds", () => {
    const tab = parse(
      stepItemHTML({
        title: "No prompt",
        state: "pending",
        detail: "main.md",
        action: { label: "Write the prompt", attrs: { "data-check-tab": "prompts" } },
      }),
    );
    const lines = Array.from(tab.querySelectorAll(".step-body > p"), (p) => p.className);
    expect(lines).toEqual(["step-title", "step-action", "step-detail"]);
    const btn = tab.querySelector(".step-action > button");
    expect(btn?.className).toBe("btn btn-link");
    expect(btn?.getAttribute("type")).toBe("button");
    expect(btn?.getAttribute("data-check-tab")).toBe("prompts");
    expect(btn?.textContent).toBe("Write the prompt");

    const page = parse(
      stepItemHTML({
        title: "No key",
        state: "pending",
        action: { label: "Set up a key", href: "/settings/#llm" },
      }),
    );
    const link = page.querySelector(".step-action > a");
    expect(link?.className).toBe("btn btn-link");
    expect(link?.getAttribute("href")).toBe("/settings/#llm");
  });

  it("escapes its label", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const el = parse(stepItemHTML({ title: "x", state: "pending", action: { label: evil } }));
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector(".step-action")?.textContent).toBe(evil);
  });
});

describe("stepFinishHTML", () => {
  it("puts the command after the link, never inside it", () => {
    const el = parse(
      `<p>${stepFinishHTML({ text: "Open settings", href: "/settings/", command: "thulu keys mint" })}</p>`,
    );
    expect(el.querySelector("a")?.textContent).toBe("Open settings");
    expect(el.querySelector("a code")).toBeNull();
    expect(el.querySelector("code.code-inline")?.textContent).toBe("thulu keys mint");
  });
});

describe("stepListHTML", () => {
  it("writes an ordered list of the steps", () => {
    const el = parse(
      stepListHTML(
        [
          { title: "One", state: "done" },
          { title: "Two", state: "pending" },
        ],
        { compact: true, id: "first-steps-list" },
      ),
    );
    expect(el.tagName).toBe("OL");
    expect(el.id).toBe("first-steps-list");
    expect(el.className).toBe("step-list step-list-compact");
    expect(Array.from(el.children, (li) => li.className)).toEqual([
      "step-item step-item-done",
      "step-item step-item-pending",
    ]);
  });
});
