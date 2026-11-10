// Deliberately separate from lib/format.ts: merging them would change what one of the two screens
// prints. See COMPONENTS.md.

import { locale } from "../i18n";
import { fmtDecimal, usd } from "./format";

export function fmtUSD(n: number): string {
  if (!isFinite(n) || n === 0) return "$0";
  if (n < 0.01) return usd(n, 4);
  if (n < 1) return usd(n, 3);
  return usd(n, 2);
}

export function fmtNum(n: number): string {
  if (!isFinite(n)) return "—";
  if (n >= 1_000_000) return `${fmtDecimal(n / 1_000_000, 1)}M`;
  if (n >= 1_000) return `${fmtDecimal(n / 1_000, 1)}k`;
  return String(Math.round(n));
}

export function fmtPct(n: number): string {
  return isFinite(n) ? `${fmtDecimal(n * 100, 1)}%` : "—";
}

export function fmtDur(ms: number): string {
  if (!isFinite(ms) || ms <= 0) return "—";
  const s = ms / 1000;
  if (s < 60) return `${fmtDecimal(s, s < 10 ? 1 : 0)}s`;
  const m = s / 60;
  if (m < 60) return `${fmtDecimal(m, m < 10 ? 1 : 0)}m`;
  const h = m / 60;
  if (h < 48) return `${fmtDecimal(h, h < 10 ? 1 : 0)}h`;
  return `${fmtDecimal(h / 24, 1)}d`;
}

export function fmtLatency(ms: number): string {
  if (!isFinite(ms) || ms < 0) return "—";
  if (ms < 1) return `${fmtDecimal(ms, 2)}ms`;
  if (ms < 1000) return `${fmtDecimal(ms, ms < 100 ? 1 : 0)}ms`;
  return fmtDur(ms);
}

export function fmtWhen(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "—" : d.toLocaleString(locale);
}

// datetime-local has no zone and the user means their own clock; sent raw, Go's time.Parse reads it
// as UTC and shifts the window.
export function toInstant(v: string): string {
  const d = new Date(v);
  return isNaN(d.getTime()) ? "" : d.toISOString();
}

export function presetRange(key: string, now = new Date()): { from: Date; to: Date } {
  const to = new Date(now);
  const from = new Date(now);
  if (key === "24h") from.setHours(from.getHours() - 24);
  else if (key === "7d") from.setDate(from.getDate() - 7);
  else if (key === "90d") from.setDate(from.getDate() - 90);
  else from.setDate(from.getDate() - 30);
  return { from, to };
}
