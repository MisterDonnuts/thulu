import { locale } from "../i18n";

export type TimeUnit = "millisecond" | "second" | "minute" | "hour" | "day";

const unitFormats = new Map<string, Intl.NumberFormat>();
let agoFormat: Intl.RelativeTimeFormat | undefined;

export function unitShort(n: number, unit: TimeUnit, digits = 0, pad = 1): string {
  const id = `${unit}:${digits}:${pad}`;
  let f = unitFormats.get(id);
  if (!f) {
    f = new Intl.NumberFormat(locale, {
      style: "unit",
      unit,
      unitDisplay: "narrow",
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
      minimumIntegerDigits: pad,
      useGrouping: false,
    });
    unitFormats.set(id, f);
  }
  return f.format(n);
}

export function agoShort(n: number, unit: Exclude<TimeUnit, "millisecond">): string {
  agoFormat ??= new Intl.RelativeTimeFormat(locale, { style: "narrow" });
  return agoFormat.format(-n, unit);
}

export function formatSeconds(n: number): string {
  const pair = (a: number, ua: TimeUnit, b: number, ub: TimeUnit) =>
    `${unitShort(a, ua)} ${unitShort(b, ub)}`;
  if (n < 60) return unitShort(n, "second");
  if (n < 3600) return pair(Math.floor(n / 60), "minute", n % 60, "second");
  if (n < 86400) return pair(Math.floor(n / 3600), "hour", Math.floor((n % 3600) / 60), "minute");
  return pair(Math.floor(n / 86400), "day", Math.floor((n % 86400) / 3600), "hour");
}

export function ago(iso: string): string {
  const then = new Date(iso).getTime();
  const secs = Math.max(0, Math.floor((Date.now() - then) / 1000));
  return formatSeconds(secs);
}

export function uptime(iso?: string): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (!isFinite(then) || then <= 0) return "—";
  const secs = Math.max(0, Math.floor((Date.now() - then) / 1000));
  return formatSeconds(secs);
}

export function fmtDate(iso: string): string {
  try {
    return new Date(iso).toLocaleString(locale, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return iso;
  }
}

export function fmtDay(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime())
    ? iso
    : d.toLocaleDateString(locale, { month: "short", day: "numeric" });
}

export function fmtDateMinute(iso: string): string {
  try {
    return new Date(iso).toLocaleString(locale, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return iso;
  }
}

// Zero prints an em dash on purpose: in averages, "no duration recorded" and "instant" are the
// same.
export function fmtMs(ms: number): string {
  if (!ms) return "—";
  if (ms < 1000) return unitShort(Math.round(ms), "millisecond");
  if (ms < 60_000) return unitShort(ms / 1000, "second", 1);
  // Round to whole seconds before splitting: rounding the remainder alone turned 22m59.6s into
  // "22m60s".
  const total = Math.round(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${unitShort(m, "minute")}${unitShort(s, "second", 0, 2)}`;
}

export function runDuration(startedAt: string, finishedAt?: string): string {
  if (!finishedAt) return "—";
  const ms = new Date(finishedAt).getTime() - new Date(startedAt).getTime();
  if (ms < 1000) return unitShort(ms, "millisecond");
  return unitShort(ms / 1000, "second", 1);
}

// Not toISOString(): it converts to UTC, so "09:00" in Buenos Aires would come back as 12:00.
export function isoLocal(d: Date): string {
  const pad = (n: number) => n.toString().padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
