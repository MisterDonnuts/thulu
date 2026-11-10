import { api } from "./api";

// The person's preferences as GET /me/preferences returns them: one canonical value per key the
// daemon knows. A key it does not return was never chosen, or no longer validates.
export async function loadPreferences(
  get: (path: string) => Promise<unknown> = (p) => api.get(p),
): Promise<Record<string, unknown>> {
  const body = (await get("/me/preferences")) as { preferences?: unknown } | null;
  const prefs = body?.preferences;
  return prefs && typeof prefs === "object" && !Array.isArray(prefs)
    ? (prefs as Record<string, unknown>)
    : {};
}

export interface PreferenceSaver<T> {
  // Queues value; only the last one queued within the delay is sent.
  save(value: T): void;
  // Sends a queued value now (the page is going away).
  flush(): void;
}

// Debounced so a run of clicks (sorting a column twice, flipping the layout back) costs one PUT.
// onError hears about a value the daemon refused or never got; the page keeps showing it.
export function preferenceSaver<T>(
  key: string,
  opts: {
    delay?: number;
    put?: (path: string, body: T) => Promise<unknown>;
    onError?: (err: unknown) => void;
    timers?: Pick<typeof globalThis, "setTimeout" | "clearTimeout">;
  } = {},
): PreferenceSaver<T> {
  const delay = opts.delay ?? 400;
  const put = opts.put ?? ((p: string, b: T) => api.put(p, b));
  const timers = opts.timers ?? globalThis;
  let pending: { value: T } | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const send = () => {
    timer = null;
    if (!pending) return;
    const { value } = pending;
    pending = null;
    put(`/me/preferences/${encodeURIComponent(key)}`, value).catch((err: unknown) =>
      opts.onError?.(err),
    );
  };
  return {
    save(value) {
      pending = { value };
      if (timer !== null) timers.clearTimeout(timer);
      timer = timers.setTimeout(send, delay);
    },
    flush() {
      if (timer !== null) timers.clearTimeout(timer);
      send();
    },
  };
}
