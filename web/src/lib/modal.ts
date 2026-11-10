// Escape is handled by the single app listener in lib/keyboard.ts, which ends with closeTopModal();
// a listener here would close two layers per press.

// Links by tag: a bare [href] also matches the <use href> inside icons, which can't take focus and
// broke the Tab trap.
const FOCUSABLE =
  // i18n-ignore a CSS selector, not a message
  'input, select, textarea, button, a[href], area[href], [tabindex]:not([tabindex="-1"])';

function focusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll(FOCUSABLE)).filter(
    (el): el is HTMLElement =>
      el instanceof HTMLElement && !el.hasAttribute("disabled") && el.offsetParent !== null,
  );
}

export function isModalOpen(overlay: HTMLElement | null): boolean {
  return overlay?.classList.contains("open") ?? false;
}

let opened: HTMLElement[] = [];
const openers = new WeakMap<HTMLElement, HTMLElement>();
const bindings = new WeakMap<HTMLElement, { close: () => void; dismissable: boolean }>();

function dialogOf(overlay: HTMLElement): HTMLElement | null {
  return overlay.querySelector<HTMLElement>(".modal, .drawer");
}

// Focus is deferred a frame: the overlay is display:none until the class lands, and focus() on a
// hidden element is a no-op.
export function openModal(overlay: HTMLElement | null, focus?: HTMLElement | null): void {
  if (!overlay) return;
  const wasOpen = isModalOpen(overlay);
  const active = document.activeElement;
  if (!wasOpen && active instanceof HTMLElement && active !== document.body) {
    if (!overlay.contains(active)) openers.set(overlay, active);
  }
  overlay.classList.add("open");
  opened = [...opened.filter((o) => o !== overlay && o.isConnected), overlay];
  requestAnimationFrame(() => {
    if (focus) {
      focus.focus();
      return;
    }
    if (overlay.contains(document.activeElement)) return;
    const box = dialogOf(overlay);
    if (!box) return;
    if (!box.hasAttribute("tabindex")) box.tabIndex = -1;
    box.focus({ preventScroll: true });
  });
}

export function closeModal(overlay: HTMLElement | null): void {
  if (!overlay) return;
  const active = document.activeElement;
  const focusInside = !active || active === document.body || overlay.contains(active);
  overlay.classList.remove("open");
  opened = opened.filter((o) => o !== overlay && o.isConnected);
  const opener = openers.get(overlay);
  openers.delete(overlay);
  if (focusInside && opener?.isConnected) opener.focus({ preventScroll: true });
}

export function topModal(): HTMLElement | null {
  opened = opened.filter((o) => o.isConnected && isModalOpen(o));
  return opened[opened.length - 1] ?? null;
}

export function closeTopModal(target?: EventTarget | null): boolean {
  const top = topModal();
  if (!top) return false;
  const el = target instanceof Node ? target : null;
  const nowhere = !el || el === document.body || el === document.documentElement;
  if (!nowhere && !top.contains(el)) return false;
  const bound = bindings.get(top);
  if (!bound) {
    closeModal(top);
    return true;
  }
  if (!bound.dismissable) return false;
  bound.close();
  return true;
}

export interface BindModalOpts {
  onClose?: () => void;
  dismissable?: boolean;
  beforeClose?: () => boolean;
}

export function bindModal(
  overlay: HTMLElement | null,
  opts: BindModalOpts = {},
): {
  open: (focus?: HTMLElement | null) => void;
  close: (force?: boolean) => void;
  isOpen: () => boolean;
} {
  const dismissable = opts.dismissable ?? true;

  const close = (force = false) => {
    if (force !== true && opts.beforeClose && isModalOpen(overlay) && !opts.beforeClose()) return;
    closeModal(overlay);
    opts.onClose?.();
  };

  if (overlay) {
    bindings.set(overlay, { close: () => close(), dismissable });
    if (dismissable) {
      overlay.addEventListener("click", (e) => {
        if (e.target === overlay) close();
      });
    }
    overlay.querySelectorAll<HTMLElement>("[data-modal-close]").forEach((el) => {
      el.addEventListener("click", () => close());
    });
    overlay.addEventListener("keydown", (e) => {
      if (e.key !== "Tab" || !overlay.classList.contains("open")) return;
      const items = focusables(overlay);
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      const outside = !overlay.contains(active);
      if (e.shiftKey && (active === first || outside)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || outside)) {
        e.preventDefault();
        first.focus();
      }
    });
  }

  return {
    open: (focus?: HTMLElement | null) => openModal(overlay, focus),
    close,
    isOpen: () => isModalOpen(overlay),
  };
}

export function escapeCloses(
  ...modals: Array<{ isOpen: () => boolean; close: () => void }>
): () => boolean {
  return () => {
    const open = modals.find((m) => m.isOpen());
    if (!open) return false;
    open.close();
    return true;
  };
}
