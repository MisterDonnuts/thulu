// public/boot/theme.js reads THEME_KEY and applies the theme; keep the key and values in sync with
// it.

export type ThemePref = "system" | "light" | "dark";

export const THEME_KEY = "octopus.theme";
export const THEME_PREFS: readonly ThemePref[] = ["system", "light", "dark"];

export function parsePref(v: string | null | undefined): ThemePref {
  return v === "light" || v === "dark" ? v : "system";
}

export function readPref(storage: Pick<Storage, "getItem"> | null = safeStorage()): ThemePref {
  try {
    return parsePref(storage?.getItem(THEME_KEY));
  } catch {
    return "system";
  }
}

export function writePref(
  pref: ThemePref,
  storage: Pick<Storage, "setItem" | "removeItem"> | null = safeStorage(),
): void {
  try {
    if (pref === "system") storage?.removeItem(THEME_KEY);
    else storage?.setItem(THEME_KEY, pref);
  } catch {
    // deliberate: the choice still applies to this page, it just is not remembered
  }
}

interface BootTheme {
  apply: () => void;
}

export function setTheme(pref: ThemePref, win: Window = window): void {
  writePref(pref, safeStorage(win));
  const boot = (win as Window & { octopusTheme?: BootTheme }).octopusTheme;
  if (boot) {
    boot.apply();
    return;
  }
  const root = win.document.documentElement;
  if (pref === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", pref);
}

export function bindThemeControl(group: HTMLElement, win: Window = window): void {
  const radios = Array.from(group.querySelectorAll<HTMLInputElement>("input[data-theme-choice]"));
  const sync = (): void => {
    const current = readPref(safeStorage(win));
    for (const radio of radios) radio.checked = radio.dataset.themeChoice === current;
  };
  for (const radio of radios) {
    radio.addEventListener("change", () => {
      if (!radio.checked) return;
      setTheme(parsePref(radio.dataset.themeChoice), win);
      sync();
    });
  }
  win.addEventListener("storage", (e) => {
    if (e.key === THEME_KEY || e.key === null) sync();
  });
  sync();
}

function safeStorage(win?: Window): Storage | null {
  try {
    return (win ?? window).localStorage;
  } catch {
    return null;
  }
}
