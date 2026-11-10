import { t } from "../i18n";
import type { StepItem } from "./ui/steps";

export interface RateLimit {
  utilization: number;
}

export interface RateSummary {
  value: string;
  band: "" | "low" | "mid" | "high";
  sub: string;
}

export function rateSummary(rl: RateLimit | null | undefined): RateSummary {
  if (!rl || typeof rl.utilization !== "number" || !Number.isFinite(rl.utilization)) {
    return { value: "—", band: "", sub: t("dashboard.metrics.rate.none") };
  }
  const u = rl.utilization;
  const value = `${(u * 100).toFixed(0)}%`;
  if (u < 0.6) return { value, band: "low", sub: t("dashboard.metrics.rate.low") };
  if (u < 0.85) return { value, band: "mid", sub: t("dashboard.metrics.rate.mid") };
  return { value, band: "high", sub: t("dashboard.metrics.rate.high") };
}

export interface SetupStep {
  id: string;
  title: string;
  done: boolean;
  detail?: string;
  effect?: string;
  href?: string;
  install?: boolean;
}

export interface SetupStatus {
  complete: boolean;
  steps: SetupStep[];
}

export function showFirstSteps(status: SetupStatus | null, taskCount: number): boolean {
  if (!status || status.complete || taskCount > 0) return false;
  return workspaceSteps(status.steps).some((s) => !s.done);
}

export function workspaceSteps(steps: SetupStep[]): SetupStep[] {
  return steps.filter((s) => !s.install);
}

export function orderSteps(steps: SetupStep[]): SetupStep[] {
  return [...steps.filter((s) => !s.done), ...steps.filter((s) => s.done)];
}

export function firstStepItems(steps: SetupStep[]): StepItem[] {
  return steps.map((s) =>
    s.done
      ? { title: s.title, state: "done", detail: s.detail }
      : {
          title: s.title,
          state: "pending",
          href: s.href,
          note: s.effect ? t("dashboard.first_steps.until_then", { effect: s.effect }) : undefined,
        },
  );
}
