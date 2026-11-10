import { describe, expect, it } from "vitest";

import { icoSvg, iconClasses, iconHref } from "./icon";

describe("iconHref", () => {
  it("takes the sprite id with or without the i- prefix", () => {
    expect(iconHref("box")).toBe("#i-box");
    expect(iconHref("i-box")).toBe("#i-box");
  });
});

describe("iconClasses", () => {
  it("adds a modifier for every size but the default", () => {
    expect(iconClasses()).toBe("ico");
    expect(iconClasses("md")).toBe("ico");
    expect(iconClasses("sm")).toBe("ico ico-sm");
    expect(iconClasses("xl", "run-ico")).toBe("ico ico-xl run-ico");
  });
});

describe("icoSvg", () => {
  it("hides a decorative glyph from screen readers", () => {
    expect(icoSvg("i-power")).toBe(
      '<svg class="ico" aria-hidden="true"><use href="#i-power"/></svg>',
    );
    expect(icoSvg("power", { size: "lg" })).toBe(
      '<svg class="ico ico-lg" aria-hidden="true"><use href="#i-power"/></svg>',
    );
  });

  it("names a glyph that carries meaning alone", () => {
    expect(icoSvg("warn", { label: 'Failed "build"' })).toBe(
      '<svg class="ico" role="img" aria-label="Failed &quot;build&quot;"><use href="#i-warn"/></svg>',
    );
  });

  it("still takes the whole class attribute as a string", () => {
    expect(icoSvg("i-warn", "ico ico-lg")).toBe(
      '<svg class="ico ico-lg" aria-hidden="true"><use href="#i-warn"/></svg>',
    );
  });
});
