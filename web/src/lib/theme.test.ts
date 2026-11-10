// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { bindThemeControl, parsePref, readPref, setTheme, THEME_KEY, writePref } from "./theme";

function memoryStorage(seed: Record<string, string> = {}): Storage {
  const data = new Map(Object.entries(seed));
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => Array.from(data.keys())[i] ?? null,
    removeItem: (k) => data.delete(k),
    setItem: (k, v) => data.set(k, String(v)),
  };
}

const throwing: Storage = {
  length: 0,
  clear: () => undefined,
  key: () => null,
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

afterEach(() => {
  delete (window as Window & { octopusTheme?: unknown }).octopusTheme;
  vi.restoreAllMocks();
});

describe("parsePref", () => {
  it("keeps light and dark and reads anything else as system", () => {
    expect(parsePref("light")).toBe("light");
    expect(parsePref("dark")).toBe("dark");
    expect(parsePref("system")).toBe("system");
    expect(parsePref(null)).toBe("system");
    expect(parsePref(undefined)).toBe("system");
    expect(parsePref("Dark")).toBe("system");
    expect(parsePref("")).toBe("system");
  });
});

describe("readPref and writePref", () => {
  it("round-trips an explicit choice under octopus.theme", () => {
    const s = memoryStorage();
    writePref("light", s);
    expect(s.getItem(THEME_KEY)).toBe("light");
    expect(readPref(s)).toBe("light");
  });

  it("clears the key for system, so the page follows the OS again", () => {
    const s = memoryStorage({ [THEME_KEY]: "dark" });
    writePref("system", s);
    expect(s.getItem(THEME_KEY)).toBeNull();
    expect(readPref(s)).toBe("system");
  });

  it("falls back to system when storage throws or is missing", () => {
    expect(readPref(throwing)).toBe("system");
    expect(readPref(null)).toBe("system");
    expect(() => {
      writePref("dark", throwing);
    }).not.toThrow();
  });
});

describe("setTheme", () => {
  it("stores the choice and asks the boot script to apply it", () => {
    const apply = vi.fn();
    (window as Window & { octopusTheme?: { apply: () => void } }).octopusTheme = { apply };
    setTheme("dark");
    expect(localStorage.getItem(THEME_KEY)).toBe("dark");
    expect(apply).toHaveBeenCalledOnce();
  });

  it("sets data-theme itself when the boot script is not on the page", () => {
    setTheme("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    setTheme("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(localStorage.getItem(THEME_KEY)).toBeNull();
  });
});

describe("bindThemeControl", () => {
  function control(): HTMLElement {
    const group = document.createElement("div");
    group.innerHTML = ["system", "light", "dark"]
      .map(
        (c) =>
          `<label><input type="radio" name="theme" role="menuitemradio" data-theme-choice="${c}"></label>`,
      )
      .join("");
    document.body.replaceChildren(group);
    return group;
  }
  const checked = (group: HTMLElement) =>
    Array.from(group.querySelectorAll<HTMLInputElement>("input:checked")).map(
      (r) => r.dataset.themeChoice,
    );

  it("checks the stored choice, and System when nothing is stored", () => {
    const group = control();
    bindThemeControl(group);
    expect(checked(group)).toEqual(["system"]);

    localStorage.setItem(THEME_KEY, "dark");
    const again = control();
    bindThemeControl(again);
    expect(checked(again)).toEqual(["dark"]);
  });

  it("stores, applies and checks the radio that was chosen", () => {
    const group = control();
    bindThemeControl(group);
    group.querySelector<HTMLElement>("[data-theme-choice='light']")?.click();
    expect(localStorage.getItem(THEME_KEY)).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(checked(group)).toEqual(["light"]);

    group.querySelector<HTMLElement>("[data-theme-choice='system']")?.click();
    expect(localStorage.getItem(THEME_KEY)).toBeNull();
    expect(checked(group)).toEqual(["system"]);
  });

  it("follows a choice made in another tab", () => {
    const group = control();
    bindThemeControl(group);
    localStorage.setItem(THEME_KEY, "dark");
    window.dispatchEvent(new StorageEvent("storage", { key: THEME_KEY, newValue: "dark" }));
    expect(checked(group)).toEqual(["dark"]);
  });
});
