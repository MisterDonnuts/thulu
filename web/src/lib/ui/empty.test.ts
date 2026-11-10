// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  emptyA11y,
  emptyIconSize,
  emptyRowClasses,
  emptyRowHTML,
  emptyStateClasses,
  emptyStateHTML,
  emptySteps,
  emptyStepsHTML,
  emptyTitleTag,
  loadingText,
} from "./empty";

function parse(html: string): Element {
  const host = document.createElement(html.startsWith("<tr") ? "tbody" : "div");
  if (host.tagName === "TBODY") document.createElement("table").appendChild(host);
  host.innerHTML = html;
  return host.firstElementChild as Element;
}

describe("emptyStateClasses", () => {
  it("writes empty-state, then the size, the outline, the tone and extras", () => {
    expect(emptyStateClasses()).toBe("empty-state");
    expect(emptyStateClasses({ size: "sm" })).toBe("empty-state empty-state-sm");
    expect(emptyStateClasses({ size: "inline", dashed: true })).toBe(
      "empty-state empty-state-inline empty-state-dashed",
    );
    expect(emptyStateClasses({ tone: "danger", cls: "grid-span" })).toBe(
      "empty-state empty-state-danger grid-span",
    );
  });

  it("refuses a size or a tone that does not exist", () => {
    expect(() => emptyStateClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
    expect(() => emptyStateClasses({ tone: "warn" as never })).toThrow(/no tone "warn"/);
  });
});

describe("emptyIconSize", () => {
  it("grows the glyph with the state", () => {
    expect(emptyIconSize()).toBe("xl");
    expect(emptyIconSize("sm")).toBe("lg");
    expect(emptyIconSize("inline")).toBe("md");
  });
});

describe("emptyA11y", () => {
  it("says nothing extra for a plain empty state", () => {
    expect(emptyA11y({})).toEqual({ role: undefined, "aria-busy": undefined });
  });
  it("marks a load in progress as a busy status", () => {
    expect(emptyA11y({ loading: true })).toEqual({ role: "status", "aria-busy": "true" });
  });
  it("announces a failed load as an alert", () => {
    expect(emptyA11y({ tone: "danger" }).role).toBe("alert");
  });
  it("leaves a table row its row role", () => {
    expect(emptyA11y({ tone: "danger", loading: true, as: "tr" })).toEqual({
      role: undefined,
      "aria-busy": "true",
    });
  });
});

describe("emptyStateHTML", () => {
  it("writes the glyph, the text and the action in that order", () => {
    const el = parse(
      emptyStateHTML({
        icon: "search",
        title: "No matches",
        body: "Change the search.",
        action: { label: "Clear filters", attrs: { "data-act": "clear" } },
      }),
    );
    expect(el.tagName).toBe("DIV");
    expect(el.className).toBe("empty-state");
    expect([...el.children].map((c) => c.getAttribute("class"))).toEqual([
      "ico ico-xl empty-state-icon",
      "empty-state-text",
      "empty-state-action",
    ]);
    expect(el.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(el.querySelector("use")?.getAttribute("href")).toBe("#i-search");
    expect(el.querySelector(".empty-state-title")?.textContent).toBe("No matches");
    expect(el.querySelector(".empty-state-body")?.textContent).toBe("Change the search.");
    const btn = el.querySelector(".empty-state-action button");
    expect(btn?.className).toBe("btn");
    expect(btn?.getAttribute("data-act")).toBe("clear");
  });

  it("escapes the title and the body", () => {
    const html = emptyStateHTML({ title: "<b>x</b>", body: `"y" & z` });
    expect(html).toContain("&lt;b&gt;x&lt;/b&gt;");
    expect(html).toContain("&quot;y&quot; &amp; z");
    expect(parse(html).querySelector("b")).toBeNull();
  });

  it("writes bodyHTML and actionHTML as they are", () => {
    const el = parse(
      emptyStateHTML({
        title: "Pick an agent",
        bodyHTML: "or press <kbd>N</kbd>",
        actionHTML: '<a class="btn" href="/x">Open</a>',
      }),
    );
    expect(el.querySelector(".empty-state-body kbd")?.textContent).toBe("N");
    expect(el.querySelector(".empty-state-action a")?.getAttribute("href")).toBe("/x");
  });

  it("leaves out the parts it is not given", () => {
    expect(emptyStateHTML({ title: "Nothing" })).toBe(
      '<div class="empty-state"><div class="empty-state-text">' +
        '<p class="empty-state-title">Nothing</p></div></div>',
    );
    expect(emptyStateHTML()).toBe('<div class="empty-state"></div>');
  });

  it("keeps empty title and body elements a script fills by id", () => {
    const el = parse(
      emptyStateHTML({ titleId: "t", bodyId: "b", id: "box", attrs: { hidden: true } }),
    );
    expect(el.id).toBe("box");
    expect(el.hasAttribute("hidden")).toBe(true);
    expect(el.querySelector("#t.empty-state-title")?.textContent).toBe("");
    expect(el.querySelector("#b.empty-state-body")?.textContent).toBe("");
  });

  it("sizes the glyph with the state and adds the variants", () => {
    const sm = parse(emptyStateHTML({ size: "sm", icon: "box", title: "x" }));
    expect(sm.className).toBe("empty-state empty-state-sm");
    expect(sm.querySelector("svg")?.getAttribute("class")).toBe("ico ico-lg empty-state-icon");
    const inline = parse(emptyStateHTML({ size: "inline", dashed: true, icon: "box", title: "x" }));
    expect(inline.className).toBe("empty-state empty-state-inline empty-state-dashed");
    expect(inline.querySelector("svg")?.getAttribute("class")).toBe("ico empty-state-icon");
  });

  it("marks a failed load as an alert with the danger tone", () => {
    const el = parse(emptyStateHTML({ tone: "danger", icon: "alert", title: "Could not load" }));
    expect(el.className).toBe("empty-state empty-state-danger");
    expect(el.getAttribute("role")).toBe("alert");
  });

  it("says Loading… while loading when no title is given", () => {
    const el = parse(emptyStateHTML({ loading: true }));
    expect(el.getAttribute("aria-busy")).toBe("true");
    expect(el.getAttribute("role")).toBe("status");
    expect(el.querySelector(".empty-state-title")?.textContent).toBe(loadingText());
    expect(loadingText()).not.toBe("");
  });

  it("renders as a list item", () => {
    expect(emptyStateHTML({ as: "li", title: "x" }).startsWith('<li class="empty-state">')).toBe(
      true,
    );
  });

  it("numbers the steps of a first-run guide between the text and the action", () => {
    const el = parse(
      emptyStateHTML({
        title: "Start here",
        steps: [{ title: "Describe <it>", body: "One line" }, { title: "Run it" }],
        action: { label: "New case" },
      }),
    );
    const steps = el.querySelector("ol.empty-state-steps") as HTMLOListElement;
    expect(steps.previousElementSibling?.className).toBe("empty-state-text");
    expect(steps.nextElementSibling?.className).toBe("empty-state-action");
    const items = steps.querySelectorAll("li.empty-state-step");
    expect(items).toHaveLength(2);
    expect(items[0].querySelector(".empty-state-step-n")?.getAttribute("aria-hidden")).toBe("true");
    expect(items[0].querySelector(".empty-state-step-n")?.textContent).toBe("1");
    expect(items[0].querySelector(".empty-state-step-title")?.textContent).toBe("Describe <it>");
    expect(items[0].querySelector(".empty-state-step-body")?.textContent).toBe("One line");
    expect(items[1].querySelector(".empty-state-step-n")?.textContent).toBe("2");
    expect(items[1].querySelector(".empty-state-step-body")).toBeNull();
  });

  it("writes the title as a heading when asked, with the same class and id", () => {
    const el = parse(emptyStateHTML({ titleTag: "h2", title: "Start <here>", titleId: "s-t" }));
    const h = el.querySelector("h2.empty-state-title") as HTMLElement;
    expect(h.id).toBe("s-t");
    expect(h.textContent).toBe("Start <here>");
    expect(el.querySelector("p.empty-state-title")).toBeNull();
    expect(
      parse(emptyStateHTML({ title: "x" })).querySelector("p.empty-state-title"),
    ).not.toBeNull();
    expect(() => emptyStateHTML({ titleTag: "h1" as never, title: "x" })).toThrow(/title tag "h1"/);
    expect(emptyTitleTag()).toBe("p");
  });

  it("writes no list without steps and refuses steps off the block size", () => {
    expect(emptyStepsHTML([])).toBe("");
    expect(emptyStepsHTML(undefined)).toBe("");
    expect(() => emptySteps("sm", [{ title: "x" }])).toThrow(/block/);
    expect(() => emptyStateHTML({ size: "inline", steps: [{ title: "x" }] })).toThrow(/block/);
    expect(() => emptySteps("sm", [])).not.toThrow();
  });
});

describe("emptyRowClasses", () => {
  it("keeps the row-empty hook first", () => {
    expect(emptyRowClasses()).toBe("row-empty");
    expect(emptyRowClasses("danger", "wide")).toBe("row-empty row-empty-danger wide");
    expect(() => emptyRowClasses("warn" as never)).toThrow(/no tone/);
  });
});

describe("emptyRowHTML", () => {
  it("writes a table row with its cell spanning the columns", () => {
    expect(emptyRowHTML("Queue is empty", { cols: 6 })).toBe(
      '<tr class="row-empty"><td colspan="6">Queue is empty</td></tr>',
    );
    expect(emptyRowHTML("x")).toBe('<tr class="row-empty"><td>x</td></tr>');
  });

  it("writes markup as is when given html, the twin of the slot", () => {
    expect(emptyRowHTML("ignored", { cols: 2, html: 'None. <a href="/x">Fix</a>' })).toBe(
      '<tr class="row-empty"><td colspan="2">None. <a href="/x">Fix</a></td></tr>',
    );
  });

  it("writes a list item or a div", () => {
    expect(emptyRowHTML("No runs yet", { as: "li" })).toBe(
      '<li class="row-empty">No runs yet</li>',
    );
    expect(emptyRowHTML("x", { as: "div", id: "e", attrs: { hidden: true } })).toBe(
      '<div id="e" class="row-empty" hidden>x</div>',
    );
  });

  it("escapes the text", () => {
    expect(emptyRowHTML("<i>&</i>", { as: "li" })).toBe(
      '<li class="row-empty">&lt;i&gt;&amp;&lt;/i&gt;</li>',
    );
  });

  it("says Loading… with aria-busy while loading, and the poller hook stays", () => {
    const row = parse(emptyRowHTML(undefined, { cols: 3, loading: true }));
    expect(row.tagName).toBe("TR");
    expect(row.getAttribute("aria-busy")).toBe("true");
    expect(row.hasAttribute("role")).toBe(false);
    expect(row.matches("tr.row-empty")).toBe(true);
    expect(row.textContent).toBe(loadingText());
    const li = parse(emptyRowHTML(undefined, { as: "li", loading: true }));
    expect(li.getAttribute("role")).toBe("status");
  });

  it("marks a failed load", () => {
    const div = parse(emptyRowHTML("Daemon offline", { as: "div", tone: "danger" }));
    expect(div.className).toBe("row-empty row-empty-danger");
    expect(div.getAttribute("role")).toBe("alert");
  });
});
