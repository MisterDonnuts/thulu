// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { buttonA11y, buttonClasses, buttonEl, buttonHTML } from "./button";

describe("buttonClasses", () => {
  it("writes btn, then the variant, then the size", () => {
    expect(buttonClasses()).toBe("btn");
    expect(buttonClasses({ variant: "primary", size: "sm" })).toBe("btn btn-primary btn-sm");
    expect(buttonClasses({ variant: "danger", size: "md" })).toBe("btn btn-danger btn-md");
    expect(buttonClasses({ variant: "ghost", cls: "card-act" })).toBe("btn btn-ghost card-act");
  });

  it("gives an icon-only button the icon-btn sizes and its icon variants", () => {
    expect(buttonClasses({ iconOnly: true })).toBe("icon-btn");
    expect(buttonClasses({ iconOnly: true, size: "lg" })).toBe("icon-btn icon-btn-lg");
    expect(buttonClasses({ iconOnly: true, size: "xs", variant: "ghost" })).toBe(
      "icon-btn icon-btn-xs icon-btn-ghost",
    );
    expect(buttonClasses({ iconOnly: true, size: "sm", variant: "danger" })).toBe(
      "icon-btn icon-btn-sm icon-btn-danger",
    );
    expect(buttonClasses({ iconOnly: true, variant: "primary" })).toBe("icon-btn btn-primary");
  });

  it("refuses a size the kind of button does not have", () => {
    expect(() => buttonClasses({ size: "lg" })).toThrow(/no size "lg" for text buttons/);
    expect(() => buttonClasses({ size: "xs" })).toThrow();
    expect(() => buttonClasses({ iconOnly: true, size: "md" })).toThrow(/icon-only/);
  });

  it("writes a link variant only as a text button at the default size", () => {
    expect(buttonClasses({ variant: "link" })).toBe("btn btn-link");
    expect(buttonClasses({ variant: "link", cls: "x" })).toBe("btn btn-link x");
    expect(() => buttonClasses({ variant: "link", size: "sm" })).toThrow(/link/);
    expect(() => buttonClasses({ variant: "link", iconOnly: true })).toThrow(/link/);
  });
});

describe("buttonA11y", () => {
  it("names an icon-only button through aria-label and the tooltip", () => {
    expect(buttonA11y({ iconOnly: true, label: "Close", icon: "x" })).toMatchObject({
      "aria-label": "Close",
      title: "Close",
    });
    expect(
      buttonA11y({ iconOnly: true, label: "Close", icon: "x", title: "Close (Esc)" }).title,
    ).toBe("Close (Esc)");
  });

  it("will not build an icon-only button without a name or a glyph", () => {
    expect(() => buttonA11y({ iconOnly: true, icon: "x" })).toThrow(/needs a label/);
    expect(() => buttonA11y({ iconOnly: true, label: "Close" })).toThrow(/needs an icon/);
  });

  it("announces a single-key hint and leaves a sequence out", () => {
    expect(buttonA11y({ kbd: "N" })["aria-keyshortcuts"]).toBe("N");
    expect(buttonA11y({ kbd: "Shift+?" })["aria-keyshortcuts"]).toBe("Shift+?");
    expect(buttonA11y({ kbd: "G T" })["aria-keyshortcuts"]).toBeUndefined();
  });

  it("disables a button with the attribute and a link by dropping its href", () => {
    expect(buttonA11y({ disabled: true })).toMatchObject({ disabled: true });
    const link = buttonA11y({ href: "/tasks/", disabled: true });
    expect(link.href).toBeUndefined();
    expect(link.disabled).toBeUndefined();
    expect(link["aria-disabled"]).toBe("true");
    expect(buttonA11y({ href: "/tasks/" }).href).toBe("/tasks/");
  });
});

describe("buttonHTML", () => {
  it("writes a plain button that never submits a form by accident", () => {
    expect(buttonHTML({ label: "Save" })).toBe('<button class="btn" type="button">Save</button>');
    expect(buttonHTML({ label: "Send", type: "submit", variant: "primary" })).toBe(
      '<button class="btn btn-primary" type="submit">Send</button>',
    );
  });

  it("puts the glyph before the label and the hint after it", () => {
    expect(buttonHTML({ label: "New task", variant: "primary", icon: "plus", kbd: "N" })).toBe(
      '<button class="btn btn-primary" type="button" aria-keyshortcuts="N">' +
        '<svg class="ico" aria-hidden="true"><use href="#i-plus"/></svg>' +
        'New task<kbd aria-hidden="true">N</kbd></button>',
    );
  });

  it("writes an icon-only button with its name and no text", () => {
    expect(buttonHTML({ label: "Close", icon: "i-x", iconOnly: true, size: "lg" })).toBe(
      '<button class="icon-btn icon-btn-lg" type="button" title="Close" aria-label="Close">' +
        '<svg class="ico" aria-hidden="true"><use href="#i-x"/></svg></button>',
    );
  });

  it("renders a link when given an href", () => {
    expect(buttonHTML({ label: "Open", href: "/tasks/42", size: "sm", variant: "ghost" })).toBe(
      '<a class="btn btn-ghost btn-sm" href="/tasks/42">Open</a>',
    );
    expect(buttonHTML({ label: "Open", href: "/tasks/42", disabled: true })).toBe(
      '<a class="btn" aria-disabled="true">Open</a>',
    );
  });

  it("passes id, placement classes and data attributes through", () => {
    expect(
      buttonHTML({
        label: "Retry",
        size: "sm",
        id: "retry",
        cls: "run-act",
        disabled: true,
        attrs: { "data-act": "retry", "data-run": 7, hidden: true, "aria-busy": false },
      }),
    ).toBe(
      '<button id="retry" class="btn btn-sm run-act" type="button" disabled data-act="retry" data-run="7" hidden>Retry</button>',
    );
  });

  it("writes trailing markup as is, between the label and the hint", () => {
    expect(
      buttonHTML({
        label: "Tools",
        size: "sm",
        kbd: "T",
        trailingHTML: '<span class="count" data-count>2/5</span>',
      }),
    ).toBe(
      '<button class="btn btn-sm" type="button" aria-keyshortcuts="T">Tools' +
        '<span class="count" data-count>2/5</span><kbd aria-hidden="true">T</kbd></button>',
    );
  });

  it("escapes every value it writes", () => {
    const html = buttonHTML({
      label: '<img src=x onerror="1">',
      title: 'a"b',
      href: '/x?a=1&b="2"',
      attrs: { "data-name": "<b>" },
    });
    expect(html).toBe(
      '<a class="btn" href="/x?a=1&amp;b=&quot;2&quot;" title="a&quot;b" data-name="&lt;b&gt;">' +
        "&lt;img src=x onerror=&quot;1&quot;&gt;</a>",
    );
    expect(buttonHTML({ label: "<x>", icon: "x", iconOnly: true })).toContain(
      'aria-label="&lt;x&gt;"',
    );
  });

  it("parses into one button with the label as its accessible text", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = buttonHTML({ label: "Delete", variant: "danger", size: "sm", kbd: "G T" });
    const btn = tpl.content.firstElementChild as HTMLButtonElement;
    expect(tpl.content.children).toHaveLength(1);
    expect(btn.tagName).toBe("BUTTON");
    expect(btn.querySelector("kbd")?.getAttribute("aria-hidden")).toBe("true");
    expect(btn.hasAttribute("aria-keyshortcuts")).toBe(false);
  });
});

describe("buttonEl", () => {
  it("is buttonHTML() as a detached element", () => {
    const b = buttonEl({ label: "Switch", size: "sm", attrs: { "data-id": "w1" } });
    expect(b).toBeInstanceOf(HTMLButtonElement);
    expect(b.outerHTML).toBe(
      buttonHTML({ label: "Switch", size: "sm", attrs: { "data-id": "w1" } }),
    );
    expect(b.isConnected).toBe(false);
    expect(b.type).toBe("button");
  });

  it("returns a link for an href", () => {
    const a = buttonEl({ label: "Docs", href: "/docs/" });
    expect(a).toBeInstanceOf(HTMLAnchorElement);
    expect(a.getAttribute("href")).toBe("/docs/");
  });

  it("builds the element buttonHTML() writes, ready for a listener", () => {
    const opts = {
      label: "Stop",
      icon: "power",
      iconOnly: true as const,
      variant: "danger" as const,
      title: "Stop",
    };
    const btn = buttonEl(opts);
    expect(btn).toBeInstanceOf(HTMLButtonElement);
    expect(btn.outerHTML).toBe(
      buttonHTML(opts).replace('<use href="#i-power"/>', '<use href="#i-power"></use>'),
    );
    expect(btn.type).toBe("button");
    expect(btn.getAttribute("aria-label")).toBe("Stop");
  });

  it("keeps the type and the placement class it is given", () => {
    const save = buttonEl({ label: "Save", size: "sm", type: "submit", cls: "prewarm-now" });
    expect(save.className).toBe("btn btn-sm prewarm-now");
    expect(save.type).toBe("submit");
    expect(save.textContent).toBe("Save");
  });
});
