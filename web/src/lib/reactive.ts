import { pageSignal } from "./page";
import { taskCardFlagClass } from "./ui/task-card";

export const dirty = {
  isFocusInEditable(root: ParentNode = document): boolean {
    const el = document.activeElement as HTMLElement | null;
    if (!el) return false;
    if (root !== document && !(root as Node).contains(el)) return false;
    const tag = el.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
    if (el.isContentEditable) return true;
    return false;
  },
  hasOpenModal(): boolean {
    return document.querySelector(".overlay.open") !== null;
  },
  isDragging(): boolean {
    return document.querySelector(`.dragging, .${taskCardFlagClass("dragging")}`) !== null;
  },
};

export interface PollerOpts {
  interval: number;
  fetch: () => Promise<void>;
  maxBackoff?: number;
  skipWhen?: () => boolean;
  firstDelay?: number;
}

export interface Poller {
  stop(): void;
  poke(): void;
}

export function startPoller(opts: PollerOpts): Poller {
  const max = opts.maxBackoff ?? 60_000;
  const base = opts.interval;
  let cur = base;
  let timer: number | null = null;
  let stopped = false;
  let inFlight = false;
  // Set by poke(); cleared only by a fetch that actually ran, so a poke during a skipped or
  // in-flight tick is not lost.
  let pending = false;

  const schedule = (delay: number) => {
    if (stopped) return;
    if (timer !== null) clearTimeout(timer);
    timer = window.setTimeout(() => void tick(), delay);
  };

  const nextDelay = (normal: number) => (pending ? POKE_DEBOUNCE_MS : normal);

  const tick = async () => {
    if (stopped) return;
    if (document.hidden) {
      schedule(base);
      return;
    }
    if (inFlight || opts.skipWhen?.()) {
      schedule(nextDelay(base));
      return;
    }
    pending = false;
    inFlight = true;
    try {
      await opts.fetch();
      cur = base;
    } catch {
      cur = Math.min(cur * 2, max);
    } finally {
      inFlight = false;
      schedule(nextDelay(cur));
    }
  };

  const onVis = () => {
    if (!document.hidden && !inFlight) schedule(0);
  };
  document.addEventListener("visibilitychange", onVis);

  const stop = () => {
    stopped = true;
    if (timer !== null) clearTimeout(timer);
    document.removeEventListener("visibilitychange", onVis);
  };
  pageSignal().addEventListener("abort", stop, { once: true });

  schedule(opts.firstDelay ?? 0);

  return {
    stop,
    poke() {
      if (stopped) return;
      pending = true;
      schedule(POKE_DEBOUNCE_MS);
    },
  };
}

const POKE_DEBOUNCE_MS = 150;

const KEY_ATTR = "rckey";

const prevDataMap = new WeakMap<HTMLElement, unknown>();

export interface ReconcileOpts<T> {
  container: HTMLElement;
  items: T[];
  key: (item: T) => string;
  create: (item: T) => HTMLElement;
  update?: (el: HTMLElement, item: T, prev: T | null) => void;
  remove?: (el: HTMLElement, item: T | null) => void;
  skip?: (el: HTMLElement, item: T) => boolean;
}

export function reconcile<T>(opts: ReconcileOpts<T>): void {
  const { container, items, key, create, update, remove, skip } = opts;

  const existing = new Map<string, HTMLElement>();
  for (const child of Array.from(container.children)) {
    const el = child as HTMLElement;
    const k = el.dataset[KEY_ATTR];
    if (k !== undefined) existing.set(k, el);
  }

  let cursor: Node | null = container.firstChild;
  const advanceCursor = () => {
    while (
      cursor &&
      (cursor.nodeType !== 1 || (cursor as HTMLElement).dataset[KEY_ATTR] === undefined)
    ) {
      cursor = cursor.nextSibling;
    }
  };

  for (const item of items) {
    const k = key(item);
    let node = existing.get(k);

    if (node) {
      existing.delete(k);
      const isSkipped = skip?.(node, item) ?? false;
      if (!isSkipped) {
        const prev = (prevDataMap.get(node) ?? null) as T | null;
        update?.(node, item, prev);
        prevDataMap.set(node, item);
      }
      advanceCursor();
      if (cursor !== node) container.insertBefore(node, cursor);
      cursor = node.nextSibling;
    } else {
      node = create(item);
      node.dataset[KEY_ATTR] = k;
      prevDataMap.set(node, item);
      advanceCursor();
      container.insertBefore(node, cursor);
    }
  }

  for (const [, el] of existing) {
    const prev = (prevDataMap.get(el) ?? null) as T | null;
    el.remove();
    remove?.(el, prev);
  }
}

export function setText(el: Element | null | undefined, value: string): void {
  if (!el) return;
  if (el.textContent !== value) el.textContent = value;
}

export function setAttr(el: Element | null | undefined, name: string, value: string | null): void {
  if (!el) return;
  if (value === null) {
    if (el.hasAttribute(name)) el.removeAttribute(name);
  } else if (el.getAttribute(name) !== value) {
    el.setAttribute(name, value);
  }
}
