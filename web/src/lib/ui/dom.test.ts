import { describe, expect, it } from "vitest";

import { attrsHTML, esc } from "./dom";

describe("esc", () => {
  it("escapes the five characters that break markup", () => {
    expect(esc(`<a href="x">'&'</a>`)).toBe(
      "&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;",
    );
    expect(esc(null)).toBe("");
    expect(esc(undefined)).toBe("");
  });
});

describe("attrsHTML", () => {
  it("writes each attribute with a leading space, in order", () => {
    expect(attrsHTML({ "data-id": 7, class: "btn" })).toBe(' data-id="7" class="btn"');
  });

  it("writes true bare and leaves false, null and undefined out", () => {
    expect(attrsHTML({ hidden: true, disabled: false, title: null, id: undefined })).toBe(
      " hidden",
    );
    expect(attrsHTML(undefined)).toBe("");
  });

  it("escapes the values", () => {
    expect(attrsHTML({ title: `"><script>` })).toBe(' title="&quot;&gt;&lt;script&gt;"');
  });
});
