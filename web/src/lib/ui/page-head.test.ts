// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { t } from "../../i18n";
import { badgeHTML } from "./badge";
import {
  crumbClasses,
  crumbsClasses,
  crumbsHTML,
  pageBackHTML,
  pageHeadClasses,
  pageHeadHTML,
} from "./page-head";

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

describe("pageHeadClasses", () => {
  it("writes page-head, the section level, flush and the extras", () => {
    expect(pageHeadClasses()).toBe("page-head");
    expect(pageHeadClasses({ flush: true })).toBe("page-head page-head-flush");
    expect(pageHeadClasses({ level: "section", cls: "bs-detail" })).toBe(
      "page-head page-head-section bs-detail",
    );
    expect(() => pageHeadClasses({ level: "card" as never })).toThrow(/no level "card"/);
  });
});

describe("crumbsHTML", () => {
  it("writes a list of links, the last step as the current page", () => {
    expect(
      crumbsHTML([
        { label: "Projects", href: "/projects/" },
        { label: "web <shop>", id: "crumb-name" },
      ]),
    ).toBe(
      `<nav class="crumbs" aria-label="${t("ui.labels.breadcrumb")}"><ol class="crumbs-list">` +
        '<li class="crumb"><a href="/projects/">Projects</a></li>' +
        '<li class="crumb"><svg class="ico ico-sm crumb-sep" aria-hidden="true"><use href="#i-chevron-right"/></svg>' +
        '<span id="crumb-name" aria-current="page">web &lt;shop&gt;</span></li></ol></nav>',
    );
  });

  it("takes its own name and id, and links every step when the last has an href", () => {
    const nav = parse(
      crumbsHTML(
        [
          { label: "Board", href: "/tasks/" },
          { label: "Parent", href: "/tasks/?task=1", id: "dh-parent" },
        ],
        { label: "Where this task lives", id: "dr-crumbs" },
      ),
    );
    expect(nav.id).toBe("dr-crumbs");
    expect(nav.getAttribute("aria-label")).toBe("Where this task lives");
    expect(nav.querySelectorAll("a")).toHaveLength(2);
    expect(nav.querySelector("[aria-current]")).toBeNull();
    expect(nav.querySelector("#dh-parent")?.getAttribute("href")).toBe("/tasks/?task=1");
  });

  it("refuses a step without a link before the last one", () => {
    expect(() => crumbsHTML([{ label: "A" }, { label: "B" }])).toThrow(/"A" needs an href/);
  });

  it("names its sizes and steps by class, and refuses a size it does not have", () => {
    expect(crumbsClasses()).toBe("crumbs");
    expect(crumbsClasses({ size: "sm", line: true })).toBe("crumbs crumbs-sm crumbs-line");
    expect(() => crumbsClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
    expect(crumbClasses({})).toBe("crumb");
    expect(crumbClasses({ mono: true, hideNarrow: true })).toBe(
      "crumb crumb-mono crumb-hide-narrow",
    );
  });

  it("writes a step's own attributes and the action after the list", () => {
    const nav = parse(
      crumbsHTML(
        [
          { label: "Board", href: "/tasks/?board=b", hideNarrow: true },
          { label: "Parent", href: "#task-p", attrs: { "data-open-task": "p" } },
          { label: "abc123", mono: true },
        ],
        { size: "sm", line: true, actionHTML: '<button type="button">Copy</button>' },
      ),
    );
    expect(nav.className).toBe("crumbs crumbs-sm crumbs-line");
    expect(q(nav, '[data-open-task="p"]').getAttribute("href")).toBe("#task-p");
    expect(q(nav, ".crumb-hide-narrow a").textContent).toBe("Board");
    expect(q(nav, ".crumb-mono [aria-current=page]").textContent).toBe("abc123");
    const action = q(nav, ":scope > .crumbs-action");
    expect(action.previousElementSibling?.className).toBe("crumbs-list");
    expect(action.textContent).toBe("Copy");
  });
});

describe("pageHeadHTML", () => {
  it("writes the h1 alone for a bare page", () => {
    expect(pageHeadHTML({ title: "Settings" })).toBe(
      '<header class="page-head"><div class="page-head-text">' +
        '<h1 class="page-title">Settings</h1></div></header>',
    );
  });

  it("writes the subtitle with its cap, the actions and the title id", () => {
    const el = parse(
      pageHeadHTML({
        title: "Usage & cost",
        titleId: "p-title",
        subtitle: "Last <30> days",
        subMax: 720,
        actionsHTML: "<button>Refresh</button>",
        flush: true,
      }),
    );
    expect(el.className).toBe("page-head page-head-flush");
    expect(el.querySelector("h1#p-title")?.textContent).toBe("Usage & cost");
    const sub = q(el, ".page-sub");
    expect(sub.textContent).toBe("Last <30> days");
    expect(sub.getAttribute("style")).toBe("max-width:720px");
    expect(el.lastElementChild?.className).toBe("page-head-actions");
  });

  it("writes a section head: h2, back link, crumbs, above, status beside the title", () => {
    const el = parse(
      pageHeadHTML({
        level: "section",
        title: "Support",
        titleId: "bs-detail-title",
        subHTML: "3 columns, <b>0</b> tasks",
        back: { href: "/settings/#boards", label: "All boards" },
        crumbs: [{ label: "Settings", href: "/settings/" }, { label: "Support" }],
        aboveHTML: '<p class="eyebrow">x</p>',
        statusHTML: badgeHTML("default", "accent"),
      }),
    );
    expect(el.className).toBe("page-head page-head-section");
    const text = q(el, ".page-head-text");
    expect(Array.from(text.children).map((c) => c.className)).toEqual([
      "page-back",
      "crumbs",
      "eyebrow",
      "page-title-row",
      "page-sub",
    ]);
    expect(text.querySelector(".page-title-row > h2.page-title")?.id).toBe("bs-detail-title");
    expect(text.querySelector(".page-head-status .badge")?.textContent).toBe("default");
    expect(text.querySelector(".page-sub b")?.textContent).toBe("0");
    expect(el.querySelector(".page-head-actions")).toBeNull();
  });

  it("writes the back link with a turned chevron", () => {
    expect(pageBackHTML({ href: "/x/?a=1&b=2", label: "Back <home>" })).toBe(
      '<a class="page-back" href="/x/?a=1&amp;b=2">' +
        '<svg class="ico page-back-ico" aria-hidden="true"><use href="#i-chevron-right"/></svg>' +
        "Back &lt;home&gt;</a>",
    );
  });
});
