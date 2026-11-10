import { describe, expect, it } from "vitest";

import { MOD_KEY } from "../shortcuts";
import { kbdClasses, kbdGroupClasses, kbdHTML, keyShortcuts } from "./kbd";

describe("kbdClasses", () => {
  it("needs no class for the default cap", () => {
    expect(kbdClasses()).toBeUndefined();
    expect(kbdClasses("sm")).toBe("kbd-sm");
    expect(kbdClasses("md", "search-hint")).toBe("search-hint");
    expect(kbdClasses("sm", undefined, true)).toBe("kbd-sm kbd-inset");
  });
});

describe("kbdGroupClasses", () => {
  it("puts the caps against the end with align end", () => {
    expect(kbdGroupClasses()).toBe("kbd-group");
    expect(kbdGroupClasses({ align: "end", cls: "x" })).toBe("kbd-group kbd-group-end x");
  });

  it("throws for an align that does not exist and for an inset group", () => {
    // @ts-expect-error: not an align
    expect(() => kbdGroupClasses({ align: "middle" })).toThrow(/no align/);
    expect(() => kbdGroupClasses({ inset: true })).toThrow(/one key/);
  });
});

describe("kbdHTML", () => {
  it("writes one cap for one key", () => {
    expect(kbdHTML("N")).toBe("<kbd>N</kbd>");
    expect(kbdHTML("/", { size: "sm" })).toBe('<kbd class="kbd-sm">/</kbd>');
    expect(kbdHTML("N", { decorative: true })).toBe('<kbd aria-hidden="true">N</kbd>');
  });

  it("writes a group with one cap per key for a chord", () => {
    expect(kbdHTML(["G", "T"], { size: "sm" })).toBe(
      '<span class="kbd-group"><kbd class="kbd-sm">G</kbd><kbd class="kbd-sm">T</kbd></span>',
    );
    expect(kbdHTML(["G", "T"], { decorative: true, cls: "menu-keys" })).toBe(
      '<span class="kbd-group menu-keys" aria-hidden="true"><kbd>G</kbd><kbd>T</kbd></span>',
    );
  });

  it("writes the cap inside a field and the keys column of the sheet", () => {
    expect(kbdHTML("/", { size: "sm", inset: true, decorative: true })).toBe(
      '<kbd class="kbd-sm kbd-inset" aria-hidden="true">/</kbd>',
    );
    expect(kbdHTML(["G", "T"], { align: "end" })).toBe(
      '<span class="kbd-group kbd-group-end"><kbd>G</kbd><kbd>T</kbd></span>',
    );
    expect(() => kbdHTML(["G", "T"], { inset: true })).toThrow(/one key/);
  });

  it("marks the modifier so Layout can swap it for Ctrl", () => {
    expect(kbdHTML([MOD_KEY, "K"])).toBe(
      `<span class="kbd-group"><kbd data-mod-key="mod">${MOD_KEY}</kbd><kbd>K</kbd></span>`,
    );
  });

  it("escapes the keys", () => {
    expect(kbdHTML("<")).toBe("<kbd>&lt;</kbd>");
  });
});

describe("keyShortcuts", () => {
  it("passes a key or a combination and drops a sequence", () => {
    expect(keyShortcuts("N")).toBe("N");
    expect(keyShortcuts(" Control+K ")).toBe("Control+K");
    expect(keyShortcuts("G T")).toBeUndefined();
    expect(keyShortcuts("")).toBeUndefined();
    expect(keyShortcuts(undefined)).toBeUndefined();
  });
});
