// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  checkClasses,
  checkEl,
  checkGroupClasses,
  checkGroupHTML,
  checkHTML,
  checkIds,
  checkReason,
  checkType,
} from "./check";

describe("checkClasses", () => {
  it("writes check, then card or head, then the extras", () => {
    expect(checkClasses()).toBe("check");
    expect(checkClasses({ card: true })).toBe("check check-card");
    expect(checkClasses({ head: true, cls: "tool-head" })).toBe("check check-head tool-head");
  });

  it("refuses a card that is also a list head", () => {
    expect(() => checkClasses({ card: true, head: true })).toThrow(/cannot be a list head/);
  });
});

describe("checkType", () => {
  it("takes checkbox by default, and radio", () => {
    expect(checkType()).toBe("checkbox");
    expect(checkType("radio")).toBe("radio");
  });

  it("refuses any other input", () => {
    // @ts-expect-error a type the component does not draw
    expect(() => checkType("text")).toThrow(/no type "text"/);
  });
});

describe("checkIds", () => {
  it("leaves the label as the name when there is no id", () => {
    expect(checkIds({ hint: true, reason: true })).toEqual({});
  });

  it("names the input by its label and describes it by the rest", () => {
    expect(checkIds({ id: "deleg", hint: true, reason: true, errorId: "deleg-err" })).toEqual({
      label: "deleg-label",
      hint: "deleg-hint",
      reason: "deleg-reason",
      labelledBy: "deleg-label",
      describedBy: "deleg-hint deleg-reason deleg-err",
    });
  });

  it("describes nothing when the label is alone", () => {
    expect(checkIds({ id: "x" }).describedBy).toBeUndefined();
  });
});

describe("checkReason", () => {
  it("lets a reason explain a disabled check only", () => {
    expect(() => checkReason("Not wired", true)).not.toThrow();
    expect(() => checkReason(undefined, false)).not.toThrow();
    expect(() => checkReason("Not wired", false)).toThrow(/explains a disabled check/);
  });
});

describe("checkHTML", () => {
  it("writes the control on the left and the label in the body", () => {
    expect(checkHTML({ label: "Running only" })).toBe(
      '<label class="check"><input type="checkbox" class="check-input">' +
        '<span class="check-body"><span class="check-label">Running only</span></span></label>',
    );
  });

  it("escapes the label, the hint and the reason", () => {
    const html = checkHTML({
      label: "<b>x</b>",
      hint: 'say "hi"',
      disabled: true,
      reason: "a & b",
    });
    expect(html).toContain('<span class="check-label">&lt;b&gt;x&lt;/b&gt;</span>');
    expect(html).toContain('<span class="check-hint">say &quot;hi&quot;</span>');
    expect(html).toContain('<span class="check-reason">a &amp; b</span>');
  });

  it("writes markup labels and hints as given", () => {
    const html = checkHTML({ labelHTML: "<code>push</code>", hintHTML: "<a href='/x'>docs</a>" });
    expect(html).toContain('<span class="check-label"><code>push</code></span>');
    expect(html).toContain("<span class=\"check-hint\"><a href='/x'>docs</a></span>");
  });

  it("ties the label, hint and reason to the input through its id", () => {
    const html = checkHTML({
      id: "deleg",
      name: "deleg",
      label: "Let chats act",
      hint: "For 8 hours",
      checked: true,
    });
    expect(html).toBe(
      '<label class="check"><input type="checkbox" class="check-input" id="deleg" name="deleg" checked' +
        ' aria-labelledby="deleg-label" aria-describedby="deleg-hint">' +
        '<span class="check-body"><span class="check-label" id="deleg-label">Let chats act</span>' +
        '<span class="check-hint" id="deleg-hint">For 8 hours</span></span></label>',
    );
  });

  it("draws a radio card with its glyph, value and state", () => {
    const html = checkHTML({
      type: "radio",
      card: true,
      name: "source",
      value: "git",
      icon: "branch",
      label: "Git repository",
      hint: "Clone from GitHub",
      checked: true,
    });
    expect(html).toBe(
      '<label class="check check-card"><input type="radio" class="check-input" name="source" value="git" checked>' +
        '<svg class="ico check-ico" aria-hidden="true"><use href="#i-branch"/></svg>' +
        '<span class="check-body"><span class="check-label">Git repository</span>' +
        '<span class="check-hint">Clone from GitHub</span></span></label>',
    );
  });

  it("puts the page's hooks on the input and the row's on the label", () => {
    const html = checkHTML({
      label: "deploy",
      inputCls: "perm-box",
      inputAttrs: { "data-perm": "deploy" },
      cls: "perm-row",
      attrs: { "data-flag-row": "gate", title: "Deploy" },
    });
    expect(html).toContain(
      '<label class="check perm-row" data-flag-row="gate" title="Deploy"><input type="checkbox" class="check-input perm-box" data-perm="deploy">',
    );
  });

  it("writes what trails the row at its end, as given", () => {
    const html = checkHTML({ label: "web", trailingHTML: '<span class="badge">Project</span>' });
    expect(html).toMatch(
      /<\/span><\/span><span class="check-trailing"><span class="badge">Project<\/span><\/span><\/label>$/,
    );
  });

  it("disables the input and explains why", () => {
    const html = checkHTML({ id: "s", label: "api", disabled: true, reason: "Not wired" });
    expect(html).toContain(" disabled ");
    expect(html).toContain('aria-describedby="s-reason"');
    expect(html).toContain('<span class="check-reason" id="s-reason">Not wired</span>');
  });

  it("refuses a reason on a check that is on", () => {
    expect(() => checkHTML({ label: "api", reason: "Not wired" })).toThrow(/disabled check/);
  });

  it("keeps an empty error line hidden for a script to fill, and describes the input by it", () => {
    const empty = checkHTML({ id: "f", label: "Gate", errorId: "f-err" });
    expect(empty).toContain('aria-describedby="f-err"');
    expect(empty).toContain('<span class="check-error" id="f-err" role="alert" hidden></span>');
    const full = checkHTML({ label: "Gate", error: "Two gates <in> a row" });
    expect(full).toContain(
      '<span class="check-error" role="alert">Two gates &lt;in&gt; a row</span>',
    );
  });
});

describe("checkEl", () => {
  it("parses the markup into a live label", () => {
    const el = checkEl({ label: "On", checked: true });
    expect(el.tagName).toBe("LABEL");
    expect(el.querySelector<HTMLInputElement>("input")?.checked).toBe(true);
  });
});

describe("checkGroupClasses", () => {
  it("writes the group and its layout", () => {
    expect(checkGroupClasses()).toBe("check-group");
    expect(checkGroupClasses({ layout: "grid" })).toBe("check-group check-group-grid");
    expect(checkGroupClasses({ layout: "grid", wide: true, cls: "x" })).toBe(
      "check-group check-group-grid check-group-wide x",
    );
    expect(checkGroupClasses({ layout: "list" })).toBe("check-group check-list");
  });

  it("refuses a layout it does not draw, and wide off the grid", () => {
    // @ts-expect-error a layout that does not exist
    expect(() => checkGroupClasses({ layout: "row" })).toThrow(/no layout "row"/);
    expect(() => checkGroupClasses({ layout: "list", wide: true })).toThrow(/only a grid/);
  });
});

describe("checkGroupHTML", () => {
  it("makes a radio group a radiogroup and hands its type and name to the items", () => {
    const html = checkGroupHTML({
      type: "radio",
      name: "source",
      layout: "grid",
      labelledBy: "m-source-label",
      items: [
        { value: "git", card: true, label: "Git", checked: true },
        { value: "upload", card: true, label: "Upload" },
      ],
    });
    expect(
      html.startsWith(
        '<div class="check-group check-group-grid" role="radiogroup" aria-labelledby="m-source-label">',
      ),
    ).toBe(true);
    expect(html.match(/type="radio" class="check-input" name="source"/g)).toHaveLength(2);
    expect(html).toContain('value="git" checked>');
  });

  it("lets an item keep a name of its own", () => {
    const html = checkGroupHTML({ name: "a", items: [{ label: "x", name: "b" }] });
    expect(html).toContain('name="b"');
    expect(html).not.toContain('name="a"');
  });

  it("names a checkbox group and writes ready markup as given", () => {
    expect(
      checkGroupHTML({ layout: "list", label: "Grants", id: "g", itemsHTML: "<p>none</p>" }),
    ).toBe(
      '<div class="check-group check-list" id="g" role="group" aria-label="Grants"><p>none</p></div>',
    );
  });
});
