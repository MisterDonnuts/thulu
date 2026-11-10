import { describe, expect, it } from "vitest";

import {
  groupHeadClasses,
  groupHeadHTML,
  listRowAttrs,
  listRowClasses,
  listRowHTML,
  listRowsHTML,
  listRowTag,
  rowGroupBoxedClasses,
  rowGroupBoxedHTML,
} from "./list-row";

describe("listRowClasses", () => {
  it("is row, then the variants, then extras", () => {
    expect(listRowClasses()).toBe("row");
    expect(listRowClasses({ boxed: true, reveal: true, dim: true, cls: "key-row" })).toBe(
      "row row-boxed row-reveal row-dim key-row",
    );
    expect(listRowClasses({ size: "sm", wrap: true })).toBe("row row-sm row-wrap");
    expect(listRowClasses({ wrap: "all" })).toBe("row row-wrap row-wrap-all");
    expect(listRowClasses({ size: "md" })).toBe("row");
  });

  it("refuses a size that does not exist", () => {
    expect(() => listRowClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
  });

  it("clamps the meta to one or two lines, and nothing else", () => {
    expect(listRowClasses({ clamp: 1 })).toBe("row row-clamp-1");
    expect(listRowClasses({ size: "sm", wrap: true, clamp: 2 })).toBe(
      "row row-sm row-wrap row-clamp-2",
    );
    expect(() => listRowClasses({ clamp: 3 as never })).toThrow(/no clamp "3"/);
  });

  it("puts the meta on the title's line, which takes no clamp", () => {
    expect(listRowClasses({ size: "sm", inline: true })).toBe("row row-sm row-inline");
    expect(() => listRowClasses({ inline: true, clamp: 1 })).toThrow(/takes no clamp/);
  });
});

describe("listRowTag and listRowAttrs", () => {
  it("makes a row with an href a link", () => {
    expect(listRowTag({})).toBe("div");
    expect(listRowTag({ href: "/x/" })).toBe("a");
    expect(listRowTag({ as: "li" })).toBe("li");
  });

  it("gives a button row its type and its open state", () => {
    expect(listRowAttrs({ as: "button", expanded: false })).toMatchObject({
      type: "button",
      "aria-expanded": "false",
    });
  });

  it("marks the current item and the current page", () => {
    expect(listRowAttrs({ current: true })["aria-current"]).toBe("true");
    expect(listRowAttrs({ href: "/a/", current: "page" })["aria-current"]).toBe("page");
    expect(listRowAttrs({ current: false })["aria-current"]).toBeUndefined();
  });

  it("refuses the combinations that cannot work", () => {
    expect(() => listRowAttrs({ as: "a" })).toThrow(/needs href/);
    expect(() => listRowAttrs({ as: "li", href: "/x/" })).toThrow(/href on a li row/);
    expect(() => listRowAttrs({ expanded: true })).toThrow(/only a button row/);
    expect(() => listRowAttrs({ as: "button", hasActions: true })).toThrow(/actions inside/);
    expect(() => listRowAttrs({ href: "/x/", hasActions: true })).toThrow(/actions inside/);
  });
});

describe("listRowHTML", () => {
  it("puts titleAttrs on the title, the link when the title links", () => {
    const html = listRowHTML({
      title: "Fix",
      titleHref: "#task-t1",
      titleAttrs: { "data-open-task": "t1", "aria-label": "Fix, Done" },
    });
    expect(html).toContain(
      '<a class="row-title" href="#task-t1" data-open-task="t1" aria-label="Fix, Done">Fix</a>',
    );
    expect(listRowHTML({ title: "x", titleAttrs: { id: "t" } })).toContain(
      '<span class="row-title" id="t">x</span>',
    );
  });

  it("writes a dense row whose title wraps", () => {
    expect(listRowHTML({ as: "li", size: "sm", wrap: true, title: "x" })).toMatch(
      /^<li class="row row-sm row-wrap">/,
    );
  });

  it("writes the four zones in order", () => {
    expect(
      listRowHTML({
        leadHTML: "<i>L</i>",
        title: "deploy-bot",
        badgesHTML: "<b>B</b>",
        meta: "developer",
        aside: "2 h",
        actionsHTML: "<select></select>",
      }),
    ).toBe(
      '<div class="row"><span class="row-lead"><i>L</i></span>' +
        '<span class="row-main"><span class="row-top"><span class="row-title">deploy-bot</span><b>B</b></span>' +
        '<span class="row-meta">developer</span></span>' +
        '<span class="row-aside">2 h</span>' +
        '<span class="row-actions"><select></select></span></div>',
    );
  });

  it("writes the tags under the meta, outside the part a clamp cuts", () => {
    expect(
      listRowHTML({ as: "button", title: "dev", meta: "Writes", clamp: 2, tagsHTML: "<b>T</b>" }),
    ).toBe(
      '<button class="row row-clamp-2" type="button"><span class="row-main">' +
        '<span class="row-top"><span class="row-title">dev</span></span>' +
        '<span class="row-meta">Writes</span><span class="row-tags"><b>T</b></span></span></button>',
    );
  });

  it("leaves out the zones it has nothing for", () => {
    expect(listRowHTML({ title: "a" })).toBe(
      '<div class="row"><span class="row-main"><span class="row-top"><span class="row-title">a</span></span></span></div>',
    );
  });

  it("escapes text and keeps markup options as given", () => {
    const html = listRowHTML({ title: "<x>", meta: "a & b", metaHTML: undefined, aside: '"q"' });
    expect(html).toContain("&lt;x&gt;");
    expect(html).toContain("a &amp; b");
    expect(html).toContain("&quot;q&quot;");
    expect(listRowHTML({ titleHTML: "<code>id</code>" })).toContain(
      '<span class="row-title"><code>id</code></span>',
    );
  });

  it("draws every action as a sm button", () => {
    const html = listRowHTML({
      title: "k",
      actions: [
        { label: "Rotate", size: "md" },
        { label: "Revoke", variant: "danger" },
        { label: "Delete", icon: "trash", iconOnly: true },
      ],
    });
    expect(html).toContain('<button class="btn btn-sm" type="button">Rotate</button>');
    expect(html).toContain('class="btn btn-danger btn-sm"');
    expect(html).toContain('class="icon-btn icon-btn-sm"');
  });

  it("links the title, not the row, when the row holds actions", () => {
    const html = listRowHTML({
      title: "t1",
      titleHref: "/tasks/?t=1",
      mono: true,
      actions: [{ label: "Open" }],
    });
    expect(html).toContain('<a class="row-title row-title-mono" href="/tasks/?t=1">t1</a>');
  });

  it("writes a toggle row with its caret", () => {
    const html = listRowHTML({ as: "button", title: "Batch", expanded: true, current: true });
    expect(
      html.startsWith(
        '<button class="row" type="button" aria-current="true" aria-expanded="true">',
      ),
    ).toBe(true);
    expect(html).toContain('<span class="row-caret"><svg class="ico ico-sm"');
  });

  it("puts id, the variants and the caller's attributes on the row", () => {
    const html = listRowHTML({
      title: "x",
      id: "k1",
      dim: true,
      boxed: true,
      cls: "key-row",
      attrs: { "data-id": "k1", hidden: true },
    });
    expect(
      html.startsWith('<div id="k1" class="row row-boxed row-dim key-row" data-id="k1" hidden>'),
    ).toBe(true);
  });

  it("needs a title", () => {
    expect(() => listRowHTML({ meta: "m" })).toThrow(/needs a title/);
  });
});

describe("listRowsHTML", () => {
  it("wraps the rows in a row-list and makes them items of a ul", () => {
    expect(listRowsHTML([{ title: "a" }, { title: "b" }], { as: "ul", id: "keys" })).toBe(
      '<ul id="keys" class="row-list">' +
        '<li class="row"><span class="row-main"><span class="row-top"><span class="row-title">a</span></span></span></li>' +
        '<li class="row"><span class="row-main"><span class="row-top"><span class="row-title">b</span></span></span></li></ul>',
    );
  });
});

describe("groupHeadHTML", () => {
  it("writes glyph, kicker, title, meta and extra", () => {
    expect(
      groupHeadHTML({
        icon: "folder",
        kicker: "System",
        title: "tienda",
        meta: "3 cases",
        extraHTML: "<i>bar</i>",
        level: 3,
      }),
    ).toBe(
      '<div class="group-head"><svg class="ico" aria-hidden="true"><use href="#i-folder"/></svg>' +
        '<h3 class="group-head-title"><span class="group-head-kicker">System</span>tienda</h3>' +
        '<span class="group-head-meta">3 cases</span><span class="group-head-extra"><i>bar</i></span></div>',
    );
  });

  it("fills one th of a table row, with the attributes on the tr", () => {
    expect(
      groupHeadHTML({ as: "tr", cols: 5, band: true, title: "<T>", attrs: { "data-group": "g" } }),
    ).toBe(
      '<tr class="group-head-row" data-group="g"><th colspan="5"><div class="group-head group-head-band">' +
        '<span class="group-head-title">&lt;T&gt;</span></div></th></tr>',
    );
  });

  it("links the title to the group's page, escaped", () => {
    expect(groupHeadHTML({ title: "<T>", href: "/tasks/#task-t1", level: 3 })).toBe(
      '<div class="group-head"><h3 class="group-head-title">' +
        '<a class="group-head-link" href="/tasks/#task-t1">&lt;T&gt;</a></h3></div>',
    );
  });

  it("refuses a heading level that is not 2, 3 or 4", () => {
    expect(() => groupHeadHTML({ title: "x", level: 5 as never })).toThrow(/no heading level 5/);
    expect(groupHeadClasses({ band: true, cls: "x" })).toBe("group-head group-head-band x");
  });
});

describe("rowGroupBoxedHTML", () => {
  it("is row-group-boxed, then the edge, then extras", () => {
    expect(rowGroupBoxedClasses()).toBe("row-group-boxed");
    expect(rowGroupBoxedClasses({ edge: true, cls: "pr-row pr-row-ready" })).toBe(
      "row-group-boxed row-group-boxed-edge pr-row pr-row-ready",
    );
  });

  it("boxes a row with its detail and carries the attributes", () => {
    const row = listRowHTML({ as: "button", size: "sm", expanded: false, title: "PR" });
    const html = rowGroupBoxedHTML(`${row}<div class="row-detail" hidden></div>`, {
      cls: "att-group",
      attrs: { "data-key": "p1" },
    });
    expect(html.startsWith('<div class="row-group-boxed att-group" data-key="p1"><button')).toBe(
      true,
    );
    expect(html.endsWith('<div class="row-detail" hidden></div></div>')).toBe(true);
  });
});
