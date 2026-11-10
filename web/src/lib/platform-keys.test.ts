// @vitest-environment jsdom
// Every modifier cap the dashboard draws reads ⌘ and ⌥ on Apple platforms
// and Ctrl and Alt elsewhere, whichever helper drew it: kbdHTML, a button's
// hint, a menu item's chord, or markup rendered at build time and relabeled
// by applyPlatformKeys(). The test draws them all and walks every <kbd>.

import { afterEach, describe, expect, it, vi } from "vitest";

import { t } from "../i18n";
import {
  ALT_KEY,
  MOD_KEY,
  applyPlatformKeys,
  ariaKeyShortcuts,
  chordText,
  globalRows,
  hintKeys,
  keyLabel,
  onApplePlatform,
} from "./shortcuts";
import { buttonHTML, kbdHTML, menuItemHTML } from "./ui";

const MAC = "MacIntel";
const LINUX = "Linux x86_64";

function on(platform: string) {
  vi.stubGlobal("navigator", { platform, userAgent: "" });
}

function drawAll(): string {
  const sheet = globalRows()
    .flatMap((r) => ("keys" in r ? [kbdHTML(r.keys)] : []))
    .join("");
  return [
    sheet,
    kbdHTML("⌘S"),
    kbdHTML([MOD_KEY, "Enter"]),
    buttonHTML({ label: "Save", kbd: `${MOD_KEY}S` }),
    menuItemHTML({ title: "Move left", kbd: [ALT_KEY, "←"] }),
  ].join("");
}

function caps(html: string): string[] {
  document.body.innerHTML = html;
  return Array.from(document.querySelectorAll("kbd"), (k) => k.textContent);
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("platform keys", () => {
  it("draws Ctrl and Alt off Apple platforms, in every kind of cap", () => {
    on(LINUX);
    const all = caps(drawAll());
    expect(all.length).toBeGreaterThan(5);
    for (const cap of all) expect(cap).not.toMatch(/[⌘⌥]/);
    expect(all).toContain(t("common.kbd.ctrl"));
    expect(all).toContain(t("common.kbd.alt"));
  });

  it("draws ⌘ and ⌥ on a Mac", () => {
    on(MAC);
    const all = caps(drawAll());
    expect(all).toContain(MOD_KEY);
    expect(all).toContain(ALT_KEY);
    expect(all).not.toContain(t("common.kbd.ctrl"));
  });

  it("relabels caps rendered at build time once the page runs off a Mac", () => {
    on(MAC);
    const built = drawAll();
    on(LINUX);
    document.body.innerHTML = built;
    applyPlatformKeys();
    const all = Array.from(document.querySelectorAll("kbd"), (k) => k.textContent);
    for (const cap of all) expect(cap).not.toMatch(/[⌘⌥]/);
    expect(document.querySelectorAll("kbd[data-mod-key]").length).toBeGreaterThan(2);
  });

  it("writes a shortcut inside a sentence the way the platform names it", () => {
    expect(chordText([MOD_KEY, "C"], true)).toBe("⌘C");
    expect(chordText([MOD_KEY, "C"], false)).toBe(`${t("common.kbd.ctrl")}+C`);
    expect(chordText("⌥ →", false)).toBe(`${t("common.kbd.alt")}+→`);
  });

  it("splits a hint off its modifier and keeps other hints whole", () => {
    expect(hintKeys("⌘S")).toEqual([MOD_KEY, "S"]);
    expect(hintKeys("⌥ →")).toEqual([ALT_KEY, "→"]);
    expect(hintKeys("G T")).toEqual(["G T"]);
    expect(hintKeys("⌘")).toEqual(["⌘"]);
  });

  it("names both modifiers for assistive tech, and no sequence", () => {
    expect(ariaKeyShortcuts("⌘S")).toBe("Meta+S Control+S");
    expect(ariaKeyShortcuts([ALT_KEY, "←"])).toBe("Alt+ArrowLeft");
    expect(ariaKeyShortcuts("G T")).toBeUndefined();
    expect(ariaKeyShortcuts("N")).toBe("N");
  });

  it("treats a page rendered at build time as Apple until a browser says otherwise", () => {
    expect(onApplePlatform(null)).toBe(true);
    expect(onApplePlatform(LINUX)).toBe(false);
    expect(keyLabel("K", false)).toBe("K");
  });
});
