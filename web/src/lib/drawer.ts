import { bindModal, isModalOpen, type BindModalOpts } from "./modal";

export type BindDrawerOpts = BindModalOpts;

export interface DrawerControls {
  open: (focus?: HTMLElement | null) => void;
  close: (force?: boolean) => void;
  isOpen: () => boolean;
}

export function bindDrawer(el: HTMLElement | null, opts: BindDrawerOpts = {}): DrawerControls {
  if (!el || el.classList.contains("overlay")) return bindModal(el, opts);
  return bindDocked(el, opts);
}

const openers = new WeakMap<HTMLElement, HTMLElement>();

function bindDocked(box: HTMLElement, opts: BindDrawerOpts): DrawerControls {
  const isOpen = () => isModalOpen(box);

  const open = (focus?: HTMLElement | null) => {
    const active = document.activeElement;
    if (!isOpen() && active instanceof HTMLElement && active !== document.body) {
      if (!box.contains(active)) openers.set(box, active);
    }
    box.classList.add("open");
    // Next frame: the box is visibility:hidden until the class applies, and focus() on a hidden
    // element is a no-op.
    requestAnimationFrame(() => {
      if (focus) {
        focus.focus();
        return;
      }
      if (box.contains(document.activeElement)) return;
      if (!box.hasAttribute("tabindex")) box.tabIndex = -1;
      box.focus({ preventScroll: true });
    });
  };

  const close = (force = false) => {
    if (!isOpen()) return;
    if (force !== true && opts.beforeClose && !opts.beforeClose()) return;
    const active = document.activeElement;
    const focusInside = !active || active === document.body || box.contains(active);
    box.classList.remove("open");
    const opener = openers.get(box);
    openers.delete(box);
    if (focusInside && opener?.isConnected) opener.focus({ preventScroll: true });
    opts.onClose?.();
  };

  box.querySelectorAll<HTMLElement>("[data-modal-close]").forEach((el) => {
    el.addEventListener("click", () => close());
  });

  return { open, close, isOpen };
}

export interface DrawerResizeOpts {
  storageKey?: string;
  min?: number;
  keep?: number;
  signal?: AbortSignal;
}

export function clampDrawerWidth(w: number, min: number, keep: number, viewport: number): number {
  const max = Math.max(min, viewport - keep);
  return Math.max(min, Math.min(max, Math.round(w)));
}

function readWidth(key: string | undefined): number {
  if (!key) return 0;
  try {
    return Number(localStorage.getItem(key)) || 0;
  } catch {
    return 0;
  }
}

function writeWidth(key: string | undefined, w: number | null): void {
  if (!key) return;
  try {
    if (w === null) localStorage.removeItem(key);
    else localStorage.setItem(key, String(w));
  } catch {
    // Storage off (a private window): the width lasts until reload.
  }
}

export function bindDrawerResize(
  box: HTMLElement | null,
  opts: DrawerResizeOpts = {},
): { reset: () => void } {
  const grip = box?.querySelector<HTMLElement>(":scope > .drawer-resizer");
  if (!box || !grip) return { reset: () => undefined };
  const min = opts.min ?? 380;
  const keep = opts.keep ?? 160;
  const dir = box.classList.contains("drawer-left") ? 1 : -1;

  const clamp = (w: number) => clampDrawerWidth(w, min, keep, window.innerWidth);
  const current = () => box.getBoundingClientRect().width;
  const report = (w: number) => {
    grip.setAttribute("aria-valuemin", String(min));
    grip.setAttribute("aria-valuemax", String(clamp(Infinity)));
    grip.setAttribute("aria-valuenow", String(Math.round(w)));
  };
  const apply = (w: number) => {
    box.style.setProperty("width", `${w}px`);
    report(w);
  };

  const stored = readWidth(opts.storageKey);
  if (stored > 0) apply(clamp(stored));
  else report(current());

  let startX = 0;
  let startW = 0;
  grip.addEventListener("pointerdown", (e: PointerEvent) => {
    e.preventDefault();
    e.stopPropagation();
    startX = e.clientX;
    startW = current();
    box.dataset.resizing = "true";
    grip.setPointerCapture(e.pointerId);
  });
  grip.addEventListener("pointermove", (e: PointerEvent) => {
    if (box.dataset.resizing !== "true") return;
    apply(clamp(startW + dir * (e.clientX - startX)));
  });
  const end = (e: PointerEvent) => {
    if (box.dataset.resizing !== "true") return;
    delete box.dataset.resizing;
    if (grip.hasPointerCapture(e.pointerId)) grip.releasePointerCapture(e.pointerId);
    writeWidth(opts.storageKey, clamp(current()));
  };
  grip.addEventListener("pointerup", end);
  grip.addEventListener("pointercancel", end);

  const reset = () => {
    writeWidth(opts.storageKey, null);
    box.style.removeProperty("width");
    report(current());
  };
  grip.addEventListener("dblclick", (e) => {
    e.stopPropagation();
    reset();
  });

  grip.addEventListener("keydown", (e: KeyboardEvent) => {
    let w: number;
    if (e.key === "Home") w = clamp(0);
    else if (e.key === "End") w = clamp(Infinity);
    else if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      const step = e.shiftKey ? 48 : 16;
      w = clamp(current() + (e.key === "ArrowRight" ? 1 : -1) * dir * step);
    } else return;
    e.preventDefault();
    apply(w);
    writeWidth(opts.storageKey, w);
  });

  window.addEventListener(
    "resize",
    () => {
      const cur = current();
      if (cur > clamp(cur)) apply(clamp(cur));
    },
    { signal: opts.signal },
  );

  return { reset };
}
