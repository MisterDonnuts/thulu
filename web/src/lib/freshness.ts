import { t } from "../i18n";
import { agoShort } from "./time";

export interface Freshness {
  stale: boolean;
  failures: number;
  lastError: string;
  lastSuccess: number;
}

export function freshState(): Freshness {
  return { stale: false, failures: 0, lastError: "", lastSuccess: 0 };
}

export interface KeepFreshOpts<T> {
  current: T;
  fetch: () => Promise<T>;
  freshness: Freshness;
  now?: () => number;
}

// Re-throws on purpose: the caller's poller can only back off if the rejection reaches it.
export async function keepFresh<T>(opts: KeepFreshOpts<T>): Promise<T> {
  const now = opts.now ?? (() => Date.now());
  try {
    const next = await opts.fetch();
    opts.freshness.stale = false;
    opts.freshness.failures = 0;
    opts.freshness.lastError = "";
    opts.freshness.lastSuccess = now();
    return next;
  } catch (err) {
    opts.freshness.stale = true;
    opts.freshness.failures += 1;
    opts.freshness.lastError = err instanceof Error ? err.message : String(err);
    throw err;
  }
}

export function staleNotice(f: Freshness, now: number = Date.now()): string {
  if (!f.stale) return "";
  if (f.lastSuccess === 0) return t("lib.freshness.stale.never");
  const secs = Math.max(0, Math.round((now - f.lastSuccess) / 1000));
  const mins = Math.round(secs / 60);
  const ago =
    secs < 60
      ? agoShort(secs, "second")
      : mins < 60
        ? agoShort(mins, "minute")
        : agoShort(Math.round(mins / 60), "hour");
  return t("lib.freshness.stale.since", { ago });
}
