import { locale, t } from "../i18n";
import { fmtBytes } from "./infra";

export interface Entitlements {
  plan: string;
  seat_limit: number;
  concurrency_limit: number;
  run_quota_monthly: number;
  storage_quota_bytes: number;
  runs?: { period: string; used: number; limit: number; remaining: number; resets_at: string };
  storage?: { used_bytes: number; quota_bytes: number; remaining_bytes: number };
}

export interface RunQuota {
  used: number;
  limit: number;
  value: string;
  ratio: number;
  resets: string;
  tone: "ok" | "warn" | "full";
}

export function runQuota(e: Entitlements | null | undefined): RunQuota | null {
  const runs = e?.runs;
  if (!runs || !(runs.limit > 0)) return null;
  const ratio = Math.min(1, runs.used / runs.limit);
  const when = new Date(runs.resets_at);
  return {
    used: runs.used,
    limit: runs.limit,
    value: runsValue(runs.used, runs.limit),
    ratio,
    resets: isNaN(when.getTime())
      ? ""
      : t("lib.plan.resets", {
          date: when.toLocaleDateString(locale, {
            month: "short",
            day: "numeric",
            timeZone: "UTC",
          }),
        }),
    tone: runs.used >= runs.limit ? "full" : ratio >= 0.8 ? "warn" : "ok",
  };
}

export function runsValue(used: number, limit: number): string {
  return t("lib.plan.values.runs", {
    used: used.toLocaleString(locale),
    limit: limit.toLocaleString(locale),
  });
}

export function planName(plan: string): string {
  switch (plan) {
    case "free":
      return t("lib.plan.names.free");
    case "team":
      return t("lib.plan.names.team");
    case "business":
      return t("lib.plan.names.business");
    case "enterprise":
      return t("lib.plan.names.enterprise");
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
    !(e.run_quota_monthly > 0) &&
    !(e.storage_quota_bytes > 0)
  );
}

export interface PlanFact {
  id: "runs" | "concurrency" | "seats" | "storage";
  label: string;
  value: string;
}

export function planFacts(e: Entitlements): PlanFact[] {
  const none = t("lib.plan.no_limit");
  const storageUsed = e.storage?.used_bytes ?? 0;
  return [
    {
      id: "runs",
      label: t("lib.plan.facts.runs"),
      value: e.run_quota_monthly > 0 ? runsValue(e.runs?.used ?? 0, e.run_quota_monthly) : none,
    },
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
  ];
}
