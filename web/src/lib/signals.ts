import { locale, t } from "../i18n";
import { agoShort } from "./time";

const percentFmt = new Intl.NumberFormat(locale, {
  style: "unit",
  unit: "percent",
  minimumFractionDigits: 1,
  maximumFractionDigits: 1,
});
const megabyteFmt = new Intl.NumberFormat(locale, {
  style: "unit",
  unit: "megabyte",
  maximumFractionDigits: 0,
});

export interface SeriesPoint {
  service: string;
  signal: string;
  bucket: string;
  value: number;
}

export interface SignalMapping {
  service: string;
  signal: string;
}

// Pushed signals arrive normalized by our decoder, so their unit is fixed here.
// Do not fall back to DEFAULT_UNITS: pull units follow the cloud API (an ALB reports seconds).
export const PUSHED_UNITS: Record<string, string> = {
  latency_p50: "ms",
  latency_p95: "ms",
  error_rate: "percent",
  cpu: "percent",
  memory: "percent",
  throughput: "rps",
};

export const DEFAULT_UNITS: Record<string, string | undefined> = {
  latency_p50: "ms",
  latency_p95: "ms",
  cpu: "percent",
  memory: "percent",
  cost_daily: "usd",
};

export const SIGNAL_LABELS: Record<string, string> = {
  latency_p50: t("lib.signals.labels.latency_p50"),
  latency_p95: t("lib.signals.labels.latency_p95"),
  error_rate: t("lib.signals.labels.error_rate"),
  throughput: t("lib.signals.labels.throughput"),
  cpu: t("lib.signals.labels.cpu"),
  memory: t("lib.signals.labels.memory"),
  restarts: t("lib.signals.labels.restarts"),
  saturation: t("lib.signals.labels.saturation"),
  cost_daily: t("lib.signals.labels.cost_daily"),
};

export function signalLabel(signal: string): string {
  return SIGNAL_LABELS[signal] || signal;
}

export interface SignalFormatters {
  latency: (ms: number) => string;
  usd: (n: number) => string;
  num: (n: number) => string;
}

export function fmtSignal(
  signal: string,
  value: number,
  unit: string | undefined,
  f: SignalFormatters,
): string {
  const u = unit === undefined || unit === "" ? (DEFAULT_UNITS[signal] ?? "") : unit;
  switch (u) {
    case "s":
      return f.latency(value * 1000);
    case "ms":
      return f.latency(value);
    case "percent":
      return percentFmt.format(value);
    case "usd":
      return f.usd(value);
    case "bytes":
      return megabyteFmt.format(value / 1024 / 1024);
    case "rps":
      // fmtNum rounds fractions to "0", which makes an idle service look dead.
      return value >= 1 ? `${f.num(value)}/s` : `${value.toFixed(2)}/s`;
    default:
      return f.num(value);
  }
}

export function sigAge(bucket: string, now = Date.now()): string {
  const secs = Math.round((now - new Date(bucket).getTime()) / 1000);
  if (!isFinite(secs)) return "";
  const mins = Math.floor(Math.max(0, secs) / 60);
  if (mins < 1) return t("lib.signals.age.just_now");
  if (mins < 60) return agoShort(mins, "minute");
  const hours = Math.floor(mins / 60);
  if (hours < 24) return agoShort(hours, "hour");
  return agoShort(Math.floor(hours / 24), "day");
}

export interface PushedSeries {
  service: string;
  signal: string;
  pts: SeriesPoint[];
}

// Derived from points on purpose: writing mapping rows on ingest would let a client create
// unbounded control-plane rows via service.name and bypass the approval gate.
export function pushedSignals(detail: {
  mappings: SignalMapping[];
  points: SeriesPoint[];
}): PushedSeries[] {
  // NUL separator: service.name is set by the sender, so a space or dash would merge unrelated series.
  const key = (service: string, signal: string) => `${service}\u0000${signal}`;
  const claimed = new Set(detail.mappings.map((m) => key(m.service, m.signal)));
  const groups = new Map<string, PushedSeries>();
  for (const p of detail.points) {
    const k = key(p.service, p.signal);
    if (claimed.has(k)) continue;
    const g = groups.get(k);
    if (g) g.pts.push(p);
    else groups.set(k, { service: p.service, signal: p.signal, pts: [p] });
  }
  const out = [...groups.values()];
  for (const g of out) g.pts.sort((a, b) => a.bucket.localeCompare(b.bucket));
  return out.sort((a, b) =>
    a.service === b.service ? a.signal.localeCompare(b.signal) : a.service.localeCompare(b.service),
  );
}
