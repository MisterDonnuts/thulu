// Moving transition:persist elements into the new document resets the scroll of everything inside
// them; this restores it across the swap.

const PERSISTED = "[data-astro-transition-persist]";

interface Saved {
  el: Element;
  top: number;
  left: number;
}

export function scrolledIn(roots: Iterable<Element>): Saved[] {
  const saved: Saved[] = [];
  for (const root of roots) {
    for (const el of [root, ...root.querySelectorAll("*")]) {
      if (el.scrollTop || el.scrollLeft) saved.push({ el, top: el.scrollTop, left: el.scrollLeft });
    }
  }
  return saved;
}

export function restore(saved: Saved[]): void {
  for (const { el, top, left } of saved) {
    el.scrollTop = top;
    el.scrollLeft = left;
  }
}

if (typeof document !== "undefined") {
  let saved: Saved[] = [];
  document.addEventListener("astro:before-swap", () => {
    saved = scrolledIn(document.querySelectorAll(PERSISTED));
  });
  document.addEventListener("astro:after-swap", () => {
    restore(saved);
    saved = [];
  });
}
