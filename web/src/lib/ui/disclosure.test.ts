// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { codeBlockHTML } from "./code";
import { disclosureClasses, disclosureHTML } from "./disclosure";

function parse(html: string): HTMLDetailsElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLDetailsElement;
}

describe("disclosureClasses", () => {
  it("writes disclosure, the variant, ruled, then the extras", () => {
    expect(disclosureClasses()).toBe("disclosure disclosure-help");
    expect(disclosureClasses({ variant: "fields" })).toBe("disclosure disclosure-fields");
    expect(disclosureClasses({ variant: "code", ruled: true, cls: "x" })).toBe(
      "disclosure disclosure-code disclosure-ruled x",
    );
  });

  it("has a fields variant for a form's optional fields", () => {
    expect(disclosureClasses({ variant: "fields", ruled: true })).toBe(
      "disclosure disclosure-fields disclosure-ruled",
    );
  });

  it("has a row variant for a line of a list that opens in place", () => {
    expect(disclosureClasses({ variant: "row", cls: "tc-case" })).toBe(
      "disclosure disclosure-row tc-case",
    );
  });

  it("throws on a variant that does not exist", () => {
    expect(() => disclosureClasses({ variant: "json" as never })).toThrow(/no variant "json"/);
  });
});

describe("disclosureHTML", () => {
  it("writes a closed details with a sprite caret and the body in a paragraph", () => {
    expect(disclosureHTML({ summary: "Why?", body: "Because." })).toBe(
      '<details class="disclosure disclosure-help"><summary class="disclosure-summary">' +
        '<svg class="ico disclosure-caret" aria-hidden="true"><use href="#i-chevron-right"/></svg>' +
        '<span>Why?</span></summary><div class="disclosure-body"><p>Because.</p></div></details>',
    );
  });

  it("opens, and takes ids and attributes", () => {
    const el = parse(
      disclosureHTML({
        summary: "s",
        open: true,
        id: "disks-help",
        bodyId: "disks-help-text",
        attrs: { hidden: true },
      }),
    );
    expect(el.open).toBe(true);
    expect(el.hidden).toBe(true);
    expect(el.id).toBe("disks-help");
    expect(el.querySelector(".disclosure-body")?.id).toBe("disks-help-text");
  });

  it("escapes the summary and the body", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const el = parse(disclosureHTML({ summary: evil, body: evil }));
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector("summary")?.textContent).toBe(evil);
    expect(el.querySelector(".disclosure-body")?.textContent).toBe(evil);
  });

  it("holds a code block as its body, and prefers body when both are given", () => {
    const code = codeBlockHTML({ text: '{"a":1}', multiline: true, maxHeight: "sm" });
    const el = parse(disclosureHTML({ variant: "code", summary: "Raw", bodyHTML: code }));
    expect(el.querySelector(".disclosure-body pre.code-block-text")?.textContent).toBe('{"a":1}');
    expect(
      parse(disclosureHTML({ summary: "s", body: "t", bodyHTML: code })).querySelector("pre"),
    ).toBeNull();
  });

  it("writes summaryHTML as is when there is no summary text, and prefers the text", () => {
    const parts = '<span class="tc-row"><span class="disclosure-title">Login</span></span>';
    const el = parse(
      disclosureHTML({
        variant: "row",
        id: "tc-TC-1",
        cls: "tc-case",
        open: true,
        attrs: { "data-key": "TC-1" },
        summaryHTML: parts,
        bodyHTML: '<div class="tc-detail"></div>',
      }),
    );
    expect(el.className).toBe("disclosure disclosure-row tc-case");
    expect(el.dataset.key).toBe("TC-1");
    expect(el.open).toBe(true);
    const summary = el.querySelector("summary");
    expect(summary?.firstElementChild?.classList.contains("disclosure-caret")).toBe(true);
    expect(summary?.querySelector(":scope > span > .tc-row .disclosure-title")?.textContent).toBe(
      "Login",
    );
    expect(
      parse(disclosureHTML({ summary: "<b>", summaryHTML: "<i>x</i>" })).querySelector("i"),
    ).toBeNull();
  });
});
