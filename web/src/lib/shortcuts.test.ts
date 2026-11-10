import { describe, expect, it } from "vitest";

import { GO_TARGETS, MOD_KEY, globalRows, isApplePlatform } from "./shortcuts";

describe("global shortcuts", () => {
  it("lists exactly the G chords the handler knows", () => {
    const listed = globalRows()
      .filter((r): r is { keys: string[]; label: string } => "keys" in r && r.keys[0] === "G")
      .map((r) => r.keys[1].toLowerCase())
      .sort();
    expect(listed).toEqual(Object.keys(GO_TARGETS).sort());
  });

  it("reaches Inbox, Evidence and Settings from anywhere", () => {
    expect(GO_TARGETS.i).toBe("/inbox/");
    expect(GO_TARGETS.e).toBe("/evidence/");
    expect(GO_TARGETS.s).toBe("/settings/");
  });

  it("links pages with the trailing slash, never the API path", () => {
    for (const href of Object.values(GO_TARGETS)) expect(href.endsWith("/")).toBe(true);
  });

  it("names the palette and the help sheet", () => {
    const keys = globalRows().flatMap((r) => ("keys" in r ? [r.keys.join(" ")] : []));
    expect(keys).toContain(`${MOD_KEY} K`);
    expect(keys).toContain("?");
  });

  it("keeps ⌘ for Apple platforms only", () => {
    expect(isApplePlatform("MacIntel")).toBe(true);
    expect(isApplePlatform("iPhone")).toBe(true);
    expect(isApplePlatform("Win32")).toBe(false);
    expect(isApplePlatform("Linux x86_64")).toBe(false);
  });
});
