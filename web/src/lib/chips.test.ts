import { describe, expect, it } from "vitest";

import { agentChipHTML, chipColor, labelTagHTML, repoChipHTML } from "./chips";

describe("labelTagHTML", () => {
  it("paints the whole label in its bucket", () => {
    expect(labelTagHTML("docs")).toBe(
      `<span class="tag tag-${chipColor("docs")}"><span class="tag-label">docs</span></span>`,
    );
  });

  it("is the same colour as a picker option or with an X", () => {
    const bucket = chipColor("api");
    expect(labelTagHTML("api", { pressed: true })).toMatch(
      new RegExp(`^<button class="tag tag-${bucket}" type="button" aria-pressed="true">`),
    );
    expect(labelTagHTML("api", { removable: { attrs: { "data-tag": "api" } } })).toContain(
      'class="tag-x" aria-label="Remove api" data-tag="api"',
    );
  });
});

describe("repoChipHTML", () => {
  it("keeps the text grey and puts the bucket in the dot", () => {
    const html = repoChipHTML("tienda-web", { size: "sm" });
    expect(html).toBe(
      `<span class="tag tag-sm" title="tienda-web"><span class="tag-dot tag-dot-${chipColor("tienda-web")}" aria-hidden="true"></span><span class="tag-label">tienda-web</span></span>`,
    );
  });

  it("links to the project page with the same dot as the card", () => {
    const card = repoChipHTML("api");
    const drawer = repoChipHTML("api", { href: "/project/?id=p1" });
    expect(drawer).toMatch(/^<a class="tag" href="\/project\/\?id=p1" title="api">/);
    const dot = /<span class="tag-dot[^"]*"/.exec(card)?.[0];
    expect(dot).toBeDefined();
    expect(drawer).toContain(dot);
  });

  it("takes an X that names the repo it unlinks", () => {
    const html = repoChipHTML("api", { removable: { attrs: { "data-rm": "p1" } } });
    expect(html).toMatch(/^<span class="tag tag-removable" title="api">/);
    expect(html).toContain('aria-label="Remove api" data-rm="p1"');
  });
});

describe("agentChipHTML", () => {
  it("wears the agents' violet with the robot glyph", () => {
    expect(agentChipHTML("coder")).toBe(
      '<span class="tag tag-special"><svg class="ico" aria-hidden="true"><use href="#i-bot"/></svg><span class="tag-label">coder</span></span>',
    );
  });

  it("drops the pill inline", () => {
    expect(agentChipHTML("coder", { inline: true, size: "sm" })).toMatch(
      /^<span class="tag tag-sm tag-special tag-inline">/,
    );
  });
});
