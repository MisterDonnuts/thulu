// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { badgeHTML } from "./badge";
import { buttonHTML } from "./button";
import {
  panelBodyClasses,
  panelClasses,
  panelEl,
  panelHeadHTML,
  panelHTML,
  sectionHeadClasses,
  sectionHeadHTML,
} from "./panel";

const parse = (html: string): HTMLElement => {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
};

function q<T extends Element = HTMLElement>(root: ParentNode, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

describe("panelClasses", () => {
  it("writes panel, then the size, the tone and the extras", () => {
    expect(panelClasses()).toBe("panel");
    expect(panelClasses({ size: "sm" })).toBe("panel panel-sm");
    expect(panelClasses({ tone: "danger", cls: "acct" })).toBe("panel panel-danger acct");
    expect(panelClasses({ size: "sm", tone: "danger" })).toBe("panel panel-sm panel-danger");
    expect(panelClasses({ size: "sm", pick: true, cls: "aud-row" })).toBe(
      "panel panel-sm panel-pick aud-row",
    );
  });

  it("throws on a size or a tone that does not exist, and on a picked page panel", () => {
    expect(() => panelClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
    expect(() => panelClasses({ tone: "warn" as never })).toThrow(/no tone "warn"/);
    expect(() => panelClasses({ pick: true })).toThrow(/only a small panel is picked/);
  });
});

describe("panelBodyClasses", () => {
  it("writes the body with flush, pad md and the extras", () => {
    expect(panelBodyClasses()).toBe("panel-body");
    expect(panelBodyClasses({ flush: true })).toBe("panel-body panel-body-flush");
    expect(panelBodyClasses({ pad: "md", cls: "bs-about" })).toBe(
      "panel-body panel-body-md bs-about",
    );
  });

  it("refuses a pad on a flush body, on a small panel and one that does not exist", () => {
    expect(() => panelBodyClasses({ flush: true, pad: "md" })).toThrow(/flush body has no pad/);
    expect(() => panelBodyClasses({ size: "sm", pad: "md" })).toThrow(/one padding/);
    expect(() => panelBodyClasses({ pad: "lg" as never })).toThrow(/no pad "lg"/);
  });
});

describe("panelHTML", () => {
  it("writes a box with only a body when nothing goes in the head", () => {
    expect(panelHTML({ bodyHTML: "<p>x</p>" })).toBe(
      '<section class="panel"><div class="panel-body"><p>x</p></div></section>',
    );
    expect(panelHTML({ icon: "box", body: "x" })).not.toContain("panel-head");
  });

  it("writes the glyph, the title and the body in order, escaping text", () => {
    expect(panelHTML({ icon: "cpu", title: "CPU <now>", body: "a & b", flush: true })).toBe(
      '<section class="panel"><div class="panel-head">' +
        '<svg class="ico" aria-hidden="true"><use href="#i-cpu"/></svg>' +
        '<h2 class="panel-title">CPU &lt;now&gt;</h2></div>' +
        '<div class="panel-body panel-body-flush">a &amp; b</div></section>',
    );
  });

  it("puts status, head, aside and actions after the title, in that order", () => {
    const el = panelEl({
      title: "Version",
      titleId: "v-h",
      as: "h3",
      statusHTML: badgeHTML("failed", "warn"),
      headHTML: '<span class="legacy"></span>',
      aside: "every 12 h",
      actionsHTML: buttonHTML({ label: "Check now", size: "sm" }),
    });
    const head = q(el, ".panel-head");
    expect(Array.from(head.children).map((c) => c.className)).toEqual([
      "panel-title",
      "panel-head-status",
      "legacy",
      "panel-head-aside",
      "panel-head-actions",
    ]);
    const title = q(head, ".panel-title");
    expect(title.tagName).toBe("H3");
    expect(title.id).toBe("v-h");
    expect(head.querySelector(".panel-head-status .badge")?.textContent).toBe("failed");
    expect(head.querySelector(".panel-head-aside")?.textContent).toBe("every 12 h");
    expect(head.querySelector(".panel-head-actions .btn")?.textContent).toBe("Check now");
  });

  it("writes a head with a tray and no title", () => {
    expect(panelHeadHTML({ actionsHTML: "<button>x</button>" })).toBe(
      '<div class="panel-head"><div class="panel-head-actions"><button>x</button></div></div>',
    );
  });

  it("takes a title as markup, a div title, a foot and the box's attributes", () => {
    const el = panelEl({
      tag: "div",
      as: "div",
      titleHTML: '<input id="d-name">',
      size: "sm",
      tone: "danger",
      foot: "Measured <hourly>",
      id: "p1",
      cls: "detail",
      bodyCls: "grid",
      attrs: { "aria-labelledby": "x", hidden: true },
    });
    expect(el.tagName).toBe("DIV");
    expect(el.className).toBe("panel panel-sm panel-danger detail");
    expect(el.id).toBe("p1");
    expect(el.hidden).toBe(true);
    expect(el.getAttribute("aria-labelledby")).toBe("x");
    expect(el.querySelector("div.panel-title input#d-name")).not.toBeNull();
    expect(el.querySelector(".panel-body")?.className).toBe("panel-body grid");
    expect(el.lastElementChild?.className).toBe("panel-foot");
    expect(el.querySelector(".panel-foot")?.innerHTML).toBe("Measured &lt;hourly&gt;");
  });

  it("writes the foot as markup and the body pad", () => {
    const html = panelHTML({ pad: "md", bodyHTML: "", footHTML: "<span>a</span><span>b</span>" });
    expect(html).toBe(
      '<section class="panel"><div class="panel-body panel-body-md"></div>' +
        '<div class="panel-foot"><span>a</span><span>b</span></div></section>',
    );
  });

  it("throws on a tag or a heading it does not draw", () => {
    expect(() => panelHTML({ tag: "aside" as never })).toThrow(/no tag "aside"/);
    expect(() => panelHTML({ title: "x", as: "h5" as never })).toThrow(/no heading "h5"/);
  });

  it("writes an h4 title for a box under a section's h3", () => {
    const el = panelEl({ size: "sm", as: "h4", titleHTML: "stage <b>Review</b>", bodyHTML: "" });
    expect(el.querySelector("h4.panel-title")?.innerHTML).toBe("stage <b>Review</b>");
  });

  it("picks a small box by the check in its head and hides an empty body", () => {
    const el = panelEl({
      tag: "div",
      size: "sm",
      pick: true,
      as: "div",
      titleHTML: '<label class="check"><input class="check-input" type="checkbox"></label>',
      bodyHTML: "<div>fields</div>",
      bodyAttrs: { hidden: true, "data-aud-body": true },
    });
    expect(el.className).toBe("panel panel-sm panel-pick");
    expect(el.querySelector("div.panel-title > .check > .check-input")).not.toBeNull();
    const body = el.querySelector<HTMLElement>(".panel-body");
    expect(body?.hidden).toBe(true);
    expect(body?.hasAttribute("data-aud-body")).toBe(true);
    expect(body?.innerHTML).toBe("<div>fields</div>");
  });
});

describe("sectionHeadClasses", () => {
  it("writes sec-head, the small level and the extras", () => {
    expect(sectionHeadClasses()).toBe("sec-head");
    expect(sectionHeadClasses({ level: 4, cls: "pr-head" })).toBe("sec-head sec-head-sm pr-head");
    expect(() => sectionHeadClasses({ level: 2 as never })).toThrow(/no level "2"/);
  });
});

describe("sectionHeadHTML", () => {
  it("writes the heading with its id and the actions on the right", () => {
    expect(
      sectionHeadHTML({
        title: "Subtasks",
        id: "ov-st-h",
        asideHTML: '<span id="st-progress"></span>',
        actionsHTML: "<button>Add</button>",
      }),
    ).toBe(
      '<div class="sec-head"><h3 class="sec-title" id="ov-st-h">Subtasks</h3>' +
        '<span id="st-progress"></span><div class="sec-actions"><button>Add</button></div></div>',
    );
  });

  it("puts the count inside the heading and leaves the actions out when there are none", () => {
    const el = parse(sectionHeadHTML({ title: "Pull <requests>", count: 2, cls: "pr-head" }));
    expect(el.className).toBe("sec-head pr-head");
    expect(el.querySelector(".sec-title")?.textContent).toBe("Pull <requests>2");
    expect(el.querySelector(".sec-title .count")?.textContent).toBe("2");
    expect(el.querySelector(".sec-actions")).toBeNull();
  });

  it("writes an h4 for a subsection", () => {
    const el = parse(sectionHeadHTML({ level: 4, titleHTML: "<em>Plan</em>" }));
    expect(el.className).toBe("sec-head sec-head-sm");
    expect(el.querySelector("h4.sec-title em")?.textContent).toBe("Plan");
  });

  it("makes the title a button that folds the section", () => {
    const el = parse(
      sectionHeadHTML({
        title: "Files",
        id: "ov-files-h",
        count: 0,
        countOpts: { hideZero: true, id: "ov-files-count" },
        toggle: { id: "ov-files-toggle", controls: "ov-files-body" },
      }),
    );
    const btn = q<HTMLButtonElement>(el, "h3#ov-files-h > button.sec-toggle");
    expect(btn.type).toBe("button");
    expect(btn.id).toBe("ov-files-toggle");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.getAttribute("aria-controls")).toBe("ov-files-body");
    expect(btn.disabled).toBe(false);
    expect(Array.from(btn.children).map((c) => c.tagName.toLowerCase())).toEqual([
      "span",
      "span",
      "svg",
    ]);
    expect(btn.querySelector("#ov-files-count")?.hasAttribute("hidden")).toBe(true);
    expect(btn.querySelector("use")?.getAttribute("href")).toBe("#i-chevron-right");

    const open = q<HTMLButtonElement>(
      parse(
        sectionHeadHTML({
          title: "Files",
          toggle: { controls: "b", expanded: true, disabled: true },
        }),
      ),
      ".sec-toggle",
    );
    expect(open.getAttribute("aria-expanded")).toBe("true");
    expect(open.disabled).toBe(true);
  });
});
