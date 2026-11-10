// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";

import { t } from "../../i18n";
import { buttonHTML } from "./button";
import {
  drawerBackButton,
  drawerBodyClasses,
  drawerClasses,
  drawerEl,
  drawerHeadHTML,
  drawerHTML,
  drawerOverlayClasses,
  drawerResizerAttrs,
} from "./drawer";

function parse(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("drawerClasses", () => {
  it("writes drawer, the side, the width, docked, then the extras", () => {
    expect(drawerClasses()).toBe("drawer drawer-right drawer-md");
    expect(drawerClasses({ side: "left", size: "sm" })).toBe("drawer drawer-left drawer-sm");
    expect(drawerClasses({ size: "lg", modal: false, cls: "task" })).toBe(
      "drawer drawer-right drawer-lg drawer-docked task",
    );
  });

  it("gives a bottom sheet no width", () => {
    expect(drawerClasses({ side: "bottom" })).toBe("drawer drawer-bottom");
  });

  it("throws for a side, a size or a combination that does not exist", () => {
    expect(() => drawerClasses({ side: "top" as never })).toThrow(/no side/);
    expect(() => drawerClasses({ size: "xl" as never })).toThrow(/no size/);
    expect(() => drawerClasses({ side: "bottom", size: "md" })).toThrow(/bottom sheet/);
  });
});

describe("drawerOverlayClasses and drawerBodyClasses", () => {
  it("places the overlay against the drawer's edge", () => {
    expect(drawerOverlayClasses()).toBe("overlay overlay-drawer");
    expect(drawerOverlayClasses("left")).toBe("overlay overlay-drawer overlay-drawer-left");
    expect(drawerOverlayClasses("bottom")).toBe("overlay overlay-drawer overlay-drawer-bottom");
  });

  it("names the body's variant and keeps the page's classes", () => {
    expect(drawerBodyClasses()).toBe("drawer-body");
    expect(drawerBodyClasses("flush", "list")).toBe("drawer-body drawer-body-flush list");
    expect(drawerBodyClasses("frame")).toBe("drawer-body drawer-body-frame");
    expect(() => drawerBodyClasses("scroll" as never)).toThrow(/no body/);
  });
});

describe("drawerHTML", () => {
  it("writes a modal drawer inside the overlay, named by its heading", () => {
    const el = parse(drawerHTML({ id: "perm", title: "github <x>" }));
    expect(el.id).toBe("perm");
    expect(el.className).toBe("overlay overlay-drawer");
    expect(el.getAttribute("role")).toBe("presentation");
    const box = el.firstElementChild as HTMLElement;
    expect(box.tagName).toBe("ASIDE");
    expect(box.className).toBe("drawer drawer-right drawer-md");
    expect(box.getAttribute("role")).toBe("dialog");
    expect(box.getAttribute("aria-modal")).toBe("true");
    expect(box.getAttribute("tabindex")).toBe("-1");
    expect(box.getAttribute("aria-labelledby")).toBe("perm-title");
    expect(box.hasAttribute("aria-describedby")).toBe(false);
    const title = box.querySelector("h2.drawer-title") as HTMLElement;
    expect(title.id).toBe("perm-title");
    expect(title.textContent).toBe("github <x>");
  });

  it("writes the ✕ by default, as a lg icon button that closes", () => {
    const box = parse(drawerHTML({ id: "d", title: "T" })).firstElementChild as HTMLElement;
    const close = box.querySelector(".drawer-head-row > .icon-btn") as HTMLElement;
    expect(close.className).toBe("icon-btn icon-btn-lg");
    expect(close.getAttribute("aria-label")).toBe(t("common.buttons.close"));
    expect(close.hasAttribute("data-modal-close")).toBe(true);
    const bare = parse(drawerHTML({ id: "d", title: "T", closable: false }));
    expect(bare.querySelector("[data-modal-close]")).toBe(null);
  });

  it("docks the box itself, with the id and the attributes on it and no scrim", () => {
    const box = parse(
      drawerHTML({ id: "task", title: "", modal: false, attrs: { "data-kind": "task" } }),
    );
    expect(box.tagName).toBe("ASIDE");
    expect(box.id).toBe("task");
    expect(box.className).toBe("drawer drawer-right drawer-md drawer-docked");
    expect(box.hasAttribute("aria-modal")).toBe(false);
    expect(box.dataset.kind).toBe("task");
  });

  it("describes the drawer by its sub, which can start empty", () => {
    const box = parse(drawerHTML({ id: "d", title: "T", sub: "In review" }))
      .firstElementChild as HTMLElement;
    expect(box.getAttribute("aria-describedby")).toBe("d-sub");
    expect(box.querySelector("#d-sub")?.textContent).toBe("In review");
    const empty = parse(drawerHTML({ id: "d", title: "", sub: "" }));
    expect(empty.querySelector("p.drawer-sub")?.textContent).toBe("");
  });

  it("puts the eyebrow over the title, the actions before the ✕ and the head under the row", () => {
    const el = parse(
      drawerHTML({
        id: "c",
        title: "Checkout",
        eyebrowHTML: '<span id="key">EV-2</span>',
        actionsHTML: '<button id="more"></button>',
        headHTML: '<div id="bar"></div>',
      }),
    );
    const heading = el.querySelector(".drawer-heading") as HTMLElement;
    expect(heading.firstElementChild?.className).toBe("drawer-eyebrow");
    expect(heading.querySelector(".drawer-eyebrow #key")).not.toBe(null);
    const row = el.querySelector(".drawer-head-row") as HTMLElement;
    const kids = Array.from(row.children).map((c) => c.className);
    expect(kids).toEqual(["drawer-heading", "drawer-actions", "icon-btn icon-btn-lg"]);
    expect(el.querySelector(".drawer-head > #bar")).not.toBe(null);
  });

  it("writes the body and, only with something in it, the foot", () => {
    const el = parse(drawerHTML({ id: "d", title: "T", bodyHTML: "<ul></ul>", body: "flush" }));
    expect(el.querySelector(".drawer-body")?.className).toBe("drawer-body drawer-body-flush");
    expect(el.querySelector(".drawer-body > ul")).not.toBe(null);
    expect(el.querySelector(".drawer-foot")).toBe(null);

    const withFoot = parse(
      drawerHTML({ id: "d", title: "T", footNote: "Untick <all>", footHTML: '<button id="ok">' }),
    );
    const foot = withFoot.querySelector("footer.drawer-foot") as HTMLElement;
    expect(foot.firstElementChild?.className).toBe("drawer-foot-note");
    expect(foot.firstElementChild?.textContent).toBe("Untick <all>");
    expect(foot.querySelector("#ok")).not.toBe(null);
  });

  it("keeps a heading the page names, with the attributes it edits it by", () => {
    const box = parse(
      drawerHTML({
        id: "drawer",
        title: "",
        titleId: "dr-title",
        titleAttrs: { contenteditable: "plaintext-only", spellcheck: "false" },
        modal: false,
      }),
    );
    const h2 = box.querySelector("h2.drawer-title") as HTMLElement;
    expect(h2.id).toBe("dr-title");
    expect(box.getAttribute("aria-labelledby")).toBe("dr-title");
    expect(h2.getAttribute("contenteditable")).toBe("plaintext-only");
    expect(h2.getAttribute("spellcheck")).toBe("false");
  });

  it("writes the bar between the head and the body, only with something in it", () => {
    const box = parse(
      drawerHTML({ id: "d", title: "T", modal: false, barHTML: '<div id="tabs"></div>' }),
    );
    const kids = Array.from(box.children).map((c) => c.className);
    expect(kids).toEqual(["drawer-head", "drawer-bar", "drawer-body"]);
    expect(box.querySelector(".drawer-bar > #tabs")).not.toBe(null);
    expect(parse(drawerHTML({ id: "d", title: "T" })).querySelector(".drawer-bar")).toBe(null);
  });

  it("writes the grab zone first in a resizable drawer, as a named separator", () => {
    const box = parse(drawerHTML({ id: "d", title: "T", resizable: true, modal: false }));
    const grip = box.firstElementChild as HTMLElement;
    expect(grip.className).toBe("drawer-resizer");
    expect(grip.getAttribute("role")).toBe("separator");
    expect(grip.getAttribute("aria-orientation")).toBe("vertical");
    expect(grip.getAttribute("aria-label")).toBe(t("ui.drawer.resize"));
    expect(grip.getAttribute("tabindex")).toBe("0");
    expect(drawerResizerAttrs().title).toBe(t("ui.drawer.resize_hint"));
  });

  it("throws for a resizable bottom sheet", () => {
    expect(() => drawerHTML({ id: "d", title: "T", side: "bottom", resizable: true })).toThrow(
      /resizable/,
    );
  });

  it("places a bottom sheet's overlay and leaves its width out", () => {
    const el = parse(drawerHTML({ id: "s", title: "Properties", side: "bottom" }));
    expect(el.className).toBe("overlay overlay-drawer overlay-drawer-bottom");
    expect(el.firstElementChild?.className).toBe("drawer drawer-bottom");
  });
});

describe("drawerHeadHTML", () => {
  it("leads with a back button before the heading, then the actions and the ✕", () => {
    const el = parse(
      drawerHeadHTML({
        title: "New chat",
        titleId: "nc",
        leadHTML: buttonHTML({ ...drawerBackButton(), cls: "x-back" }),
      }),
    );
    expect(el.tagName).toBe("HEADER");
    const row = el.querySelector(".drawer-head-row") as HTMLElement;
    const kids = Array.from(row.children).map((c) => c.className);
    expect(kids).toEqual(["drawer-lead", "drawer-heading", "icon-btn icon-btn-lg"]);
    const back = row.querySelector(".drawer-lead > button") as HTMLElement;
    expect(back.getAttribute("aria-label")).toBe(t("common.buttons.back"));
    expect(back.querySelector("use")?.getAttribute("href")).toBe("#i-chevron-left");
    expect(el.querySelector("h2#nc")?.textContent).toBe("New chat");
  });

  it("writes the line under the title as markup when it holds a badge and a link", () => {
    const el = parse(
      drawerHeadHTML({
        title: "Chat",
        titleId: "ct",
        subId: "cs",
        sub: "ignored <b>",
        subHTML: '<span class="badge">On</span><span id="s"></span>',
      }),
    );
    const sub = el.querySelector("p.drawer-sub") as HTMLElement;
    expect(sub.id).toBe("cs");
    expect(sub.querySelector(".badge")?.textContent).toBe("On");
    expect(sub.textContent).not.toContain("ignored");
  });

  it("escapes a plain sub and leaves out what has nothing in it", () => {
    const html = drawerHeadHTML({ title: "<T>", titleId: "t", sub: "a <b>", closable: false });
    expect(html).toContain("&lt;T&gt;");
    expect(html).toContain('<p class="drawer-sub">a &lt;b&gt;</p>');
    for (const part of ["drawer-lead", "drawer-eyebrow", "drawer-actions", "icon-btn"])
      expect(html).not.toContain(part);
  });
});

describe("drawerBackButton", () => {
  it("is a lg chevron as tall as the ✕, named for where it goes", () => {
    expect(drawerBackButton()).toEqual({
      iconOnly: true,
      icon: "chevron-left",
      size: "lg",
      label: t("common.buttons.back"),
    });
    expect(drawerBackButton("Back to chats").label).toBe("Back to chats");
  });
});

describe("drawerEl", () => {
  it("appends the drawer to the body and returns what carries the id", () => {
    const el = drawerEl({ id: "made", title: "T" });
    expect(el.id).toBe("made");
    expect(el.parentElement).toBe(document.body);
  });
});
