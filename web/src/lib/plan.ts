import { locale, t } from "../i18n";
import { fmtBytes } from "./infra";

export interface HourUsage {
  period: string;
  used_hours: number;
  included_hours: number;
  extra_hours: number;
  extra_usd: number;
  cap_usd: number;
  ratio: number;
  resets_at: string;
  trial_ends_at?: string;
  // The trial's hours cover its 30 days and never renew.
  trial?: boolean;
}

export interface Entitlements {
  plan: string;
  seat_limit: number;
  concurrency_limit: number;
  hour_quota_monthly: number;
  storage_quota_bytes: number;
  extra_hours_cap_usd?: number;
  trial_ends_at?: string;
  hours?: HourUsage;
  storage?: { used_bytes: number; quota_bytes: number; remaining_bytes: number };
}

// Mirrors entitlements.ExtraHourUSD and the API's maxExtraCapUSD.
export const EXTRA_HOUR_USD = 0.05;
export const MAX_EXTRA_CAP_USD = 10000;

export interface HourQuota {
  used: number;
  limit: number;
  value: string;
  sub: string;
  ratio: number;
  tone: "ok" | "warn" | "full";
}

function shortDate(iso: string | undefined): string {
  if (!iso) return "";
  const when = new Date(iso);
  if (isNaN(when.getTime()) || when.getUTCFullYear() < 2000) return "";
  return when.toLocaleDateString(locale, { month: "short", day: "numeric", timeZone: "UTC" });
}

export function usd(v: number): string {
  return v.toLocaleString(locale, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: Number.isInteger(v) ? 0 : 2,
    maximumFractionDigits: 2,
  });
}

function hoursNumber(v: number): string {
  return v.toLocaleString(locale, { maximumFractionDigits: v < 10 ? 1 : 0 });
}

export function hoursValue(used: number, limit: number): string {
  return t("lib.plan.values.hours", {
    used: hoursNumber(used),
    limit: limit.toLocaleString(locale),
  });
}

// Full means new runs wait: past the included hours with no extra hours allowed, or at the cap.
export function hourQuota(e: Entitlements | null | undefined): HourQuota | null {
  const h = e?.hours;
  if (!h || !(h.included_hours > 0)) return null;
  const over = h.used_hours >= h.included_hours;
  const atCap = over && (h.cap_usd <= 0 || h.extra_usd >= h.cap_usd);
  const resets = shortDate(h.resets_at);
  let sub = h.trial
    ? t("lib.plan.trial_hours")
    : resets
      ? t("lib.plan.resets", { date: resets })
      : "";
  if (over && h.cap_usd > 0) {
    sub = t("lib.plan.extra_so_far", {
      hours: hoursNumber(h.extra_hours),
      cost: usd(h.extra_usd),
      cap: usd(h.cap_usd),
    });
  }
  return {
    used: h.used_hours,
    limit: h.included_hours,
    value: hoursValue(h.used_hours, h.included_hours),
    sub,
    ratio: Math.min(1, h.used_hours / h.included_hours),
    tone: atCap ? "full" : h.ratio >= 0.8 ? "warn" : "ok",
  };
}

export function trialEnds(e: Entitlements | null | undefined): string {
  if (!e || (e.plan !== "trial" && e.plan !== "free")) return "";
  const date = shortDate(e.hours?.trial_ends_at ?? e.trial_ends_at);
  return date ? t("lib.plan.trial_ends", { date }) : t("lib.plan.trial_not_started");
}

export function planName(plan: string): string {
  switch (plan) {
    case "trial":
    case "free":
      return t("lib.plan.names.trial");
    case "starter":
      return t("lib.plan.names.starter");
    case "team":
    case "business":
      return t("lib.plan.names.team");
    case "enterprise":
      return t("lib.plan.names.enterprise");
    case "inactive":
      return t("lib.plan.names.inactive");
    case "self-hosted":
      return t("lib.plan.names.self_hosted");
    default:
      return plan;
  }
}

export function unlimited(e: Entitlements): boolean {
  return (
    !(e.seat_limit > 0) &&
    !(e.concurrency_limit > 0) &&
    !(e.hour_quota_monthly > 0) &&
    !(e.storage_quota_bytes > 0)
  );
}

// Plans whose admins may choose a cap: the trial and an inactive plan never have extra hours, and a
// plan without an hour quota has nothing to cap.
export function extraCapEditable(e: Entitlements | null | undefined): boolean {
  if (!e || !(e.hour_quota_monthly > 0)) return false;
  return !["trial", "free", "inactive", "self-hosted"].includes(e.plan);
}

export function extraCapNow(e: Entitlements): string {
  const cap = e.hours?.cap_usd ?? e.extra_hours_cap_usd ?? 0;
  if (!(cap > 0)) return t("lib.plan.cap.none");
  return t("lib.plan.cap.now", {
    cap: usd(cap),
    hours: Math.floor(cap / EXTRA_HOUR_USD).toLocaleString(locale),
  });
}

export function parseExtraCap(raw: string): { usd: number } | { error: string } {
  const s = raw.trim().replace(",", ".");
  const n = Number(s);
  if (s === "" || !Number.isFinite(n) || n < 0 || n > MAX_EXTRA_CAP_USD) {
    return { error: t("lib.plan.cap.out_of_range", { max: usd(MAX_EXTRA_CAP_USD) }) };
  }
  return { usd: Math.round(n * 100) / 100 };
}

export interface PlanFact {
  id: "hours" | "extra" | "concurrency" | "seats" | "storage";
  label: string;
  value: string;
}

export function planFacts(e: Entitlements): PlanFact[] {
  const none = t("lib.plan.no_limit");
  const storageUsed = e.storage?.used_bytes ?? 0;
  const facts: PlanFact[] = [
    {
      id: "hours",
      label: t("lib.plan.facts.hours"),
      value:
        e.hour_quota_monthly > 0
          ? hoursValue(e.hours?.used_hours ?? 0, e.hour_quota_monthly)
          : none,
    },
  ];
  if (e.hour_quota_monthly > 0) {
    const cap = e.hours?.cap_usd ?? 0;
    facts.push({
      id: "extra",
      label: t("lib.plan.facts.extra"),
      value:
        cap > 0 ? t("lib.plan.values.extra", { cap: usd(cap) }) : t("lib.plan.values.no_extra"),
    });
  }
  facts.push(
    {
      id: "concurrency",
      label: t("lib.plan.facts.concurrency"),
      value:
        e.concurrency_limit > 0
          ? t("lib.plan.values.concurrency", { count: e.concurrency_limit })
          : none,
    },
    {
      id: "seats",
      label: t("lib.plan.facts.seats"),
      value: e.seat_limit > 0 ? t("lib.plan.values.seats", { count: e.seat_limit }) : none,
    },
    {
      id: "storage",
      label: t("lib.plan.facts.storage"),
      value:
        e.storage_quota_bytes > 0
          ? t("lib.plan.values.storage", {
              used: fmtBytes(storageUsed),
              limit: fmtBytes(e.storage_quota_bytes),
            })
          : none,
    },
  );
  return facts;
}
