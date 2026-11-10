import { locale, t } from "../i18n";
import { esc } from "./ui";

export function usd(n: number, decimals: number): string {
  return (
    "$" +
    n.toLocaleString(locale, { minimumFractionDigits: decimals, maximumFractionDigits: decimals })
  );
}

export function fmtUsd(n: number): string {
  return usd(n || 0, 2);
}

// Sub-dollar amounts keep four decimals: per-turn costs like $0.0004 would otherwise round to
// $0.00.
export function fmtUsdCompact(n: number): string {
  if (!isFinite(n) || n === 0) return usd(0, 2);
  if (n >= 1000) return `${usd(n / 1000, 1)}k`;
  if (n >= 1) return usd(n, 2);
  return usd(n, 4);
}

export function estimatedCost(
  cost: number,
  estimated = 0,
  fmt: (n: number) => string = fmtUsdCompact,
  unpriced = 0,
): { text: string; title: string } {
  const c = pricedCost(cost, estimated, fmt, unpriced);
  return { text: c.note ? `${c.text} ${c.note}` : c.text, title: c.title };
}

function pricedCost(
  cost: number,
  estimated: number,
  fmt: (n: number) => string,
  unpriced: number,
): { text: string; title: string; note: string; noteTitle: string } {
  const noteTitle = unpriced > 0 ? t("usage.unpriced.tooltip", { count: unpriced }) : "";
  if (unpriced > 0 && !(cost > 0)) {
    return { text: t("usage.unpriced.none"), title: noteTitle, note: "", noteTitle: "" };
  }
  const note = unpriced > 0 ? t("usage.unpriced.partial", { count: unpriced }) : "";
  if (!(estimated > 0)) {
    return { text: fmt(cost), title: noteTitle, note, noteTitle };
  }
  const estTitle = t("usage.estimated.tooltip", { amount: fmt(estimated) });
  return {
    text: t("usage.estimated.mark", { amount: fmt(cost) }),
    title: noteTitle ? `${estTitle} ${noteTitle}` : estTitle,
    note,
    noteTitle,
  };
}

export function costHTML(
  cost: number,
  estimated = 0,
  fmt: (n: number) => string = fmtUsdCompact,
  unpriced = 0,
): string {
  const c = pricedCost(cost, estimated, fmt, unpriced);
  if (unpriced > 0 && !(cost > 0)) {
    return `<span class="unpriced" title="${esc(c.title)}">${esc(c.text)}</span>`;
  }
  const amount =
    estimated > 0
      ? `<span class="est" title="${esc(t("usage.estimated.tooltip", { amount: fmt(estimated) }))}">${esc(c.text)}</span>`
      : esc(c.text);
  if (!c.note) return amount;
  return `${amount} <span class="unpriced" title="${esc(c.noteTitle)}">${esc(c.note)}</span>`;
}

// toLocaleString, not toFixed: toFixed always writes a point, which mixes "2.2M" with "$18,73" in
// Spanish.
export function fmtDecimal(n: number, digits: number, loc: string = locale): string {
  return (n || 0).toLocaleString(loc, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

export function fmtTokens(n: number, loc: string = locale): string {
  if (n >= 1e6) return fmtDecimal(n / 1e6, 1, loc) + "M";
  if (n >= 1e3) return fmtDecimal(n / 1e3, 1, loc) + "k";
  return String(n);
}

export function fmtCount(n: number, loc: string = locale): string {
  if (n >= 1_000_000_000) return `${fmtDecimal(n / 1_000_000_000, 2, loc)}B`;
  if (n >= 1_000_000) return `${fmtDecimal(n / 1_000_000, 2, loc)}M`;
  if (n >= 1_000) return `${fmtDecimal(n / 1_000, 1, loc)}k`;
  return n.toLocaleString(loc);
}

export function fmtPct(n: number, loc: string = locale): string {
  return `${fmtDecimal(n * 100, 1, loc)}%`;
}
