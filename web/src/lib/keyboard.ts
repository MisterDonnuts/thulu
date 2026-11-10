import { navigate } from "astro:transitions/client";
import { bindModal, closeTopModal } from "./modal";
import { pageSignal } from "./page";
import { GO_TARGETS } from "./shortcuts";

export interface KeyboardOpts {
  rawKey?: (e: KeyboardEvent) => boolean;
  onEscape?: Array<() => boolean>;
  onKey?: (e: KeyboardEvent) => boolean;
  onSearch?: () => void;
  onNew?: () => void;
}

export interface GlobalKeyboardOpts {
  onPalette?: () => void;
}

let page: KeyboardOpts = {};
let installed = false;

function helpSheet(): HTMLElement | null {
  return document.getElementById("kbd-help") ?? document.getElementById("kbd-help-global");
}

const helpBindings = new WeakMap<HTMLElement, ReturnType<typeof bindModal>>();
function helpModal(sheet: HTMLElement): ReturnType<typeof bindModal> {
  let b = helpBindings.get(sheet);
  if (!b) {
    b = bindModal(sheet);
    helpBindings.set(sheet, b);
  }
  return b;
}

export function openHelp(): void {
  const sheet = helpSheet();
  if (sheet) helpModal(sheet).open();
}

function closeHelp(): boolean {
  const open = document.querySelector<HTMLElement>("[data-kbd-help].open");
  if (!open) return false;
  helpModal(open).close();
  return true;
}

export function initKeyboard(opts: KeyboardOpts = {}): void {
  page = opts;
  pageSignal().addEventListener(
    "abort",
    () => {
      if (page === opts) page = {};
    },
    { once: true },
  );
}

export function isPaletteKey(
  e: Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey">,
): boolean {
  return (e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k";
}

export function initGlobalKeyboard(opts: GlobalKeyboardOpts = {}): void {
  if (installed) return;
  installed = true;

  // Delegated: the router swaps the sheets and the header button on every navigation.
  document.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    if (target.closest("#kbd-help-btn")) openHelp();
    else if (
      target.closest("[data-kbd-help] [data-modal-close]") ||
      target.matches("[data-kbd-help]")
    )
      closeHelp();
  });

  let goPending = false;
  let goTimer: number | null = null;
  document.addEventListener("keydown", (e) => {
    if (page.rawKey?.(e)) return;

    const target = e.target as HTMLElement | null;
    const tag = target?.tagName;
    const isTyping =
      tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target?.isContentEditable;

    if (e.key === "Escape") {
      if (closeHelp()) return;
      for (const close of page.onEscape ?? []) {
        if (close()) return;
      }
      // Stops the key so a page's own Esc listener cannot close a second layer with the same press.
      if (!e.defaultPrevented && closeTopModal(e.target)) {
        e.preventDefault();
        e.stopImmediatePropagation();
        return;
      }
    }

    if (isPaletteKey(e) && opts.onPalette) {
      e.preventDefault();
      closeHelp();
      opts.onPalette();
      return;
    }

    if (isTyping) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    if (page.onKey?.(e)) return;

    if (goPending) {
      goPending = false;
      if (goTimer !== null) {
        window.clearTimeout(goTimer);
        goTimer = null;
      }
      const href = GO_TARGETS[e.key.toLowerCase()];
      if (href) {
        e.preventDefault();
        void navigate(href);
      }
      return;
    }
    if (e.key.toLowerCase() === "g") {
      e.preventDefault();
      goPending = true;
      goTimer = window.setTimeout(() => {
        goPending = false;
      }, 900);
      return;
    }

    if (e.key === "/" && page.onSearch) {
      e.preventDefault();
      page.onSearch();
    } else if (e.key === "?") {
      e.preventDefault();
      openHelp();
    } else if (e.key.toLowerCase() === "n" && page.onNew) {
      e.preventDefault();
      page.onNew();
    }
  });
}
