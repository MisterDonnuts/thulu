import { describe, expect, it } from "vitest";

import { factHTML, factRows, factsClasses, factsHTML } from "./facts";

describe("factsClasses", () => {
  it("writes fact-list, then the layout, then boxed", () => {
    expect(factsClasses()).toBe("fact-list");
    expect(factsClasses({ layout: "grid", boxed: true, cls: "detail" })).toBe(
      "fact-list fact-list-grid fact-list-boxed detail",
    );
    expect(factsClasses({ layout: "inline" })).toBe("fact-list fact-list-inline");
  });

  it("refuses a layout that does not exist", () => {
    // @ts-expect-error: not a layout
    expect(() => factsClasses({ layout: "table" })).toThrow(/no layout "table"/);
  });
});

describe("factRows", () => {
  it("drops the rows with no value and keeps an empty string to fill later", () => {
    const rows = factRows([
      { label: "a", value: null },
      { label: "b" },
      { label: "c", value: "" },
      { label: "d", value: 0 },
      { label: "e", valueHTML: "<b>x</b>" },
    ]);
    expect(rows.map((r) => r.label)).toEqual(["c", "d", "e"]);
  });
});

describe("factsHTML", () => {
  it("writes one div per pair and escapes labels, values and notes", () => {
    expect(
      factsHTML([
        { label: "Kernel <6>", value: "6.8 & up" },
        { label: "Image", value: "sha256:ab", mono: true, note: "from $IMAGE" },
      ]),
    ).toBe(
      '<dl class="fact-list">' +
        "<div><dt>Kernel &lt;6&gt;</dt><dd>6.8 &amp; up</dd></div>" +
        '<div><dt>Image</dt><dd class="fact-list-mono">sha256:ab<span class="fact-list-note">from $IMAGE</span></dd></div>' +
        "</dl>",
    );
  });

  it("takes markup the caller escaped, and attributes on the list, the row and the value", () => {
    const html = factsHTML(
      [
        {
          label: "Client",
          value: "",
          attrs: { id: "client-fact", hidden: true },
          valueAttrs: { id: "client-id" },
          mono: true,
        },
      ],
      { layout: "inline", attrs: { id: "summary" } },
    );
    expect(html).toBe(
      '<dl class="fact-list fact-list-inline" id="summary">' +
        '<div id="client-fact" hidden><dt>Client</dt><dd class="fact-list-mono" id="client-id"></dd></div></dl>',
    );
    expect(factHTML({ label: "Repo", valueHTML: '<a href="/p">p</a>' })).toBe(
      '<div><dt>Repo</dt><dd><a href="/p">p</a></dd></div>',
    );
  });

  it("writes numbers", () => {
    expect(factHTML({ label: "Cores", value: 8 })).toContain("<dd>8</dd>");
  });
});
