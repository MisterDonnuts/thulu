// The ClientRouter runs a page's module script only once per tab, so pages register through
// onPage().
// Anything outliving the page's elements (window/document listeners, intervals, subscriptions) must
// stop on `signal`.

type Start = (signal: AbortSignal) => void | Promise<void>;

const starts = new Map<string, Start[]>();
// Per <body>, because the router swaps <body>; keeps the first load from running a body both on
// register and on astro:page-load.
const started = new WeakMap<HTMLElement, Set<Start>>();
let current: AbortController | null = null;

function normalize(path: string): string {
  return path.replace(/\/+$/, "") || "/";
}

function start(): void {
  const fns = starts.get(normalize(window.location.pathname));
  if (!fns) return;
  let done = started.get(document.body);
  if (!done) {
    done = new Set();
    started.set(document.body, done);
    current?.abort();
    current = new AbortController();
  }
  const signal = current?.signal;
  if (!signal) return; // between astro:before-swap and the swap itself
  for (const fn of fns) {
    if (done.has(fn)) continue;
    done.add(fn);
    void fn(signal);
  }
}

// The router handles same-page "#hash" links with a synthetic popstate (state null) and never fires
// hashchange; this re-dispatches it for pages that listen to hashchange.
let lastHash = "";

function bridgeHashChange(): void {
  lastHash = window.location.hash;
  window.addEventListener("hashchange", () => (lastHash = window.location.hash), true);
  window.addEventListener("popstate", (ev) => {
    if (ev.state !== null || history.state === null) return;
    if (window.location.hash === lastHash) return;
    const oldURL = new URL(window.location.href);
    oldURL.hash = lastHash;
    window.dispatchEvent(
      new HashChangeEvent("hashchange", { oldURL: oldURL.href, newURL: window.location.href }),
    );
  });
  document.addEventListener("astro:page-load", () => (lastHash = window.location.hash));
}

if (typeof document !== "undefined") {
  bridgeHashChange();
  document.addEventListener("astro:page-load", start);
  document.addEventListener("astro:before-swap", () => {
    current?.abort();
    current = null;
  });
}

// Pages must rewrite the URL through here: history.replaceState fires no event, so the hash bridge
// would keep a stale lastHash and ignore a link back to it.
export function replaceURL(url: string | URL): void {
  // eslint-disable-next-line no-restricted-syntax -- the helper itself
  history.replaceState(history.state, "", url);
  lastHash = window.location.hash;
}

export function onPage(path: string, fn: Start): void {
  const key = normalize(path);
  starts.set(key, [...(starts.get(key) ?? []), fn]);
  start();
}

export function pageSignal(): AbortSignal {
  return current?.signal ?? new AbortController().signal;
}

export function every(ms: number, fn: () => void, signal: AbortSignal): void {
  if (signal.aborted) return;
  const id = window.setInterval(fn, ms);
  signal.addEventListener("abort", () => window.clearInterval(id), { once: true });
}
